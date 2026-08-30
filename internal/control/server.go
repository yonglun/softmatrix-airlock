package control

import (
	"net/http"
)

type ServerDeps struct {
	Auth   *Auth
	OrgAPI *OrgAPI
}

type Server struct {
	deps ServerDeps
}

func NewServer(deps ServerDeps) *Server {
	return &Server{deps: deps}
}

// Handler 装配管理面路由。
// /healthz 与 /auth/* 不需要会话，/api/* 一律要求已登录。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /auth/login", s.deps.Auth.HandleLogin)
	mux.HandleFunc("GET /auth/callback", s.deps.Auth.HandleCallback)
	mux.HandleFunc("POST /auth/logout", s.deps.Auth.HandleLogout)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/whoami", func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusInternalServerError, "internal_error", "上下文缺少用户")
			return
		}
		writeJSON(w, http.StatusOK, u)
	})
	api.HandleFunc("GET /api/orgs", s.deps.OrgAPI.HandleList)
	api.HandleFunc("POST /api/orgs", s.deps.OrgAPI.HandleCreate)
	api.HandleFunc("PATCH /api/orgs/{id}/name", s.deps.OrgAPI.HandleRename)
	api.HandleFunc("PATCH /api/orgs/{id}/parent", s.deps.OrgAPI.HandleMove)
	api.HandleFunc("DELETE /api/orgs/{id}", s.deps.OrgAPI.HandleDelete)
	api.HandleFunc("GET /api/orgs/import/preview", s.deps.OrgAPI.HandleImportPreview)
	api.HandleFunc("POST /api/orgs/import/apply", s.deps.OrgAPI.HandleImportApply)

	mux.Handle("/api/", s.deps.Auth.RequireSession(api))

	return mux
}
