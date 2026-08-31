package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	sessionCookie    = "airlock_session"
	loginStateCookie = "airlock_login"

	sessionTTL    = 12 * time.Hour
	loginStateTTL = 10 * time.Minute
)

type ctxKey int

const userCtxKey ctxKey = iota

// UserFromContext 取出 RequireSession 放进上下文的用户。
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userCtxKey).(*User)
	return u, ok
}

type AuthDeps struct {
	Users          UserStore
	Sessions       SessionStore
	LoginStates    LoginStateStore
	OIDC           OIDCClient
	RBAC           RBACStore
	BootstrapAdmin string
	// SecureCookie 在生产（HTTPS）下必须为 true；本地 HTTP 调试置 false。
	SecureCookie bool
}

type Auth struct {
	deps AuthDeps
}

func NewAuth(deps AuthDeps) *Auth {
	return &Auth{deps: deps}
}

// HandleLogin 生成 state 与 PKCE，落库后重定向到 IdP。
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := NewState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "生成登录状态失败")
		return
	}
	verifier, challenge, err := NewPKCE()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "生成 PKCE 失败")
		return
	}

	ls := LoginState{
		ID:           uuid.NewString(),
		State:        state,
		PKCEVerifier: verifier,
		RedirectTo:   sanitizeRedirect(r.URL.Query().Get("redirect_to")),
		ExpiresAt:    time.Now().Add(loginStateTTL),
	}
	if err := a.deps.LoginStates.Create(r.Context(), ls); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存登录状态失败")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     loginStateCookie,
		Value:    ls.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.deps.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(loginStateTTL.Seconds()),
	})
	http.Redirect(w, r, a.deps.OIDC.AuthCodeURL(state, challenge), http.StatusFound)
}

// HandleCallback 校验 state、用授权码换身份、建会话。
func (a *Auth) HandleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(loginStateCookie)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_login_state", "缺少登录状态，请重新登录")
		return
	}

	// Take 取出即作废：同一次登录状态不可能被重放。
	ls, err := a.deps.LoginStates.Take(r.Context(), c.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_login_state", "登录状态无效或已过期，请重新登录")
		return
	}
	clearCookie(w, loginStateCookie, a.deps.SecureCookie)

	if r.URL.Query().Get("state") != ls.State {
		writeError(w, http.StatusBadRequest, "state_mismatch", "登录状态校验失败")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing_code", "回调缺少授权码")
		return
	}

	identity, err := a.deps.OIDC.Exchange(r.Context(), code, ls.PKCEVerifier)
	if err != nil {
		slog.Warn("OIDC 换取身份失败", "err", err)
		writeError(w, http.StatusBadGateway, "oidc_exchange_failed", "与身份提供方通信失败")
		return
	}

	// 登录时补一次禁用检查，覆盖「刚被禁用但还没到对账周期」的窗口。
	if existing, err := a.deps.Users.ByExternalID(r.Context(), identity.Subject); err == nil {
		if existing.Status != UserStatusActive {
			writeError(w, http.StatusForbidden, "user_disabled", "该账号已被禁用")
			return
		}
	} else if !errors.Is(err, ErrUserNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "查询用户失败")
		return
	}

	now := time.Now().UTC()
	user, err := a.deps.Users.Upsert(r.Context(), &User{
		ExternalID:  identity.Subject,
		Email:       identity.Email,
		DisplayName: identity.DisplayName,
		Status:      UserStatusActive,
		LastLoginAt: &now,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "写入用户失败")
		return
	}

	if err := a.maybeGrantBootstrapAdmin(r.Context(), user, *identity); err != nil {
		slog.Error("授予 bootstrap 管理员失败", "user_id", user.ID, "err", err)
	}

	token, hash, err := GenerateSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "生成会话失败")
		return
	}
	if err := a.deps.Sessions.Create(r.Context(), Session{
		ID:         hash,
		UserID:     user.ID,
		ExpiresAt:  now.Add(sessionTTL),
		LastSeenAt: now,
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存会话失败")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.deps.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	slog.Info("用户登录", "user_id", user.ID, "email", user.Email, "ip", clientIP(r))
	http.Redirect(w, r, ls.RedirectTo, http.StatusFound)
}

func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.deps.Sessions.Delete(r.Context(), HashSessionToken(c.Value)); err != nil {
			slog.Warn("删除会话失败", "err", err)
		}
	}
	clearCookie(w, sessionCookie, a.deps.SecureCookie)
	w.WriteHeader(http.StatusNoContent)
}

// RequireSession 是管理面 API 的鉴权中间件。
// 与 Edge 的 ak- 鉴权完全独立，两者互不感知。
func (a *Auth) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "no_session", "未登录")
			return
		}

		sess, err := a.deps.Sessions.Get(r.Context(), HashSessionToken(c.Value))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "no_session", "会话无效或已过期")
			return
		}

		user, err := a.deps.Users.ByID(r.Context(), sess.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "no_session", "会话对应的用户不存在")
			return
		}
		if user.Status != UserStatusActive {
			writeError(w, http.StatusForbidden, "user_disabled", "该账号已被禁用")
			return
		}

		if err := a.deps.Sessions.Touch(r.Context(), sess.ID, time.Now().UTC()); err != nil {
			slog.Warn("更新会话活跃时间失败", "err", err)
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, user)))
	})
}

// sanitizeRedirect 只接受站内绝对路径，防开放重定向。
//
// "//evil.com" 这种协议相对 URL 要挡掉；"/\evil.com" 同样要挡——
// 浏览器解析 http/https 这类"特殊 scheme"的相对引用时，把反斜杠当正斜杠
// 处理（WHATWG URL 标准行为），所以只查 "//" 前缀会被反斜杠绕过。
// 先把反斜杠归一化成正斜杠，再统一走同一套前缀检查，两种写法都堵。
func sanitizeRedirect(v string) string {
	v = strings.ReplaceAll(v, "\\", "/")
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return "/"
	}
	return v
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// writeError 是管理面统一的错误响应格式。
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
