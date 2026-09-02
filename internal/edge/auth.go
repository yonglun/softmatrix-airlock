package edge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/softmatrix/airlock/internal/apikey"
)

type ctxKey int

const keyCtxKey ctxKey = iota

// KeyFromContext 取出鉴权阶段放入上下文的密钥。
func KeyFromContext(ctx context.Context) (*apikey.Key, bool) {
	k, ok := ctx.Value(keyCtxKey).(*apikey.Key)
	return k, ok
}

const bearerPrefix = "Bearer "

// Authenticate 返回一个中间件：校验 ak- 密钥并把它放进请求上下文。
func Authenticate(store apikey.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("Authorization")
			if raw == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing_api_key", "请求缺少 Authorization 头")
				return
			}
			if !strings.HasPrefix(raw, bearerPrefix) {
				writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "Authorization 头必须使用 Bearer 方案")
				return
			}
			plain := strings.TrimSpace(strings.TrimPrefix(raw, bearerPrefix))

			// 先做廉价的格式校验，挡掉明显非法的输入，避免无谓查库。
			// 这里与下方"密钥不存在"分支使用完全相同的 message 文案：
			// 二者的 code/status 本就相同（防止客户端区分「格式错」与「查无此密钥」两种失败原因），
			// 文案不一致会让攻击者仍可通过消息文本探测出具体原因，因此必须保持逐字节一致。
			if err := apikey.ValidateFormat(plain); err != nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "密钥格式非法或不存在")
				return
			}

			key, err := store.ByHash(r.Context(), apikey.Hash(plain))
			if err != nil {
				if errors.Is(err, apikey.ErrKeyNotFound) {
					writeAuthError(w, http.StatusUnauthorized, "invalid_api_key", "密钥格式非法或不存在")
					return
				}
				writeAuthError(w, http.StatusInternalServerError, "internal_error", "校验密钥时发生内部错误")
				return
			}

			switch err := key.Validate(time.Now()); {
			case errors.Is(err, apikey.ErrKeyRevoked):
				writeAuthError(w, http.StatusForbidden, "key_revoked", "密钥已吊销")
				return
			case errors.Is(err, apikey.ErrKeyExpired):
				writeAuthError(w, http.StatusForbidden, "key_expired", "密钥已过期")
				return
			case err != nil:
				writeAuthError(w, http.StatusForbidden, "key_unusable", "密钥不可用")
				return
			}

			// 客户端若到窗口结束都没换上新凭据，到期瞬间会集体 401。
			// 打一条日志让运维能提前看到，而不是等故障发生。
			// 刻意只打日志、不写库：鉴权是热路径、目前零写入，
			// 为一个观测需求给每个请求加一次 DB 写不划算。
			if key.ViaPrevKey && key.PrevKeyExpiresAt != nil {
				slog.Warn("客户端仍在使用轮换前的旧凭据",
					"key_id", key.ID,
					"prev_expires_at", key.PrevKeyExpiresAt.UTC(),
					"remaining", time.Until(*key.PrevKeyExpiresAt).Round(time.Second))
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCtxKey, key)))
		})
	}
}

// writeAuthError 以 OpenAI 兼容的错误结构返回，客户端 SDK 能直接识别。
func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}
