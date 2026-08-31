package control

import (
	"net/http"
	"strings"

	"github.com/softmatrix/airlock/internal/authz"
)

type ServerDeps struct {
	Auth     *Auth
	OrgAPI   *OrgAPI
	GrantAPI *GrantAPI
	Resolver *authz.Resolver
	// Routes 供测试注入自定义路由表；为空时用 DefaultRoutes。
	Routes []Route
}

type Server struct {
	deps   ServerDeps
	routes []Route
}

func NewServer(deps ServerDeps) *Server {
	routes := deps.Routes
	if routes == nil {
		routes = DefaultRoutes(deps)
	}
	return &Server{deps: deps, routes: routes}
}

// Routes 返回本服务器的路由表，供机械检查测试使用。
func (s *Server) Routes() []Route { return s.routes }

// Handler 装配管理面路由。
//
// 注册路由只能走路由表——不要在这里直接写 mux.HandleFunc，
// routes_test.go 的机械检查会拦下那种写法。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()

	for _, rt := range s.routes {
		h := s.enforce(rt)
		if rt.Access == AccessPublic {
			mux.HandleFunc(rt.Pattern, h)
			continue
		}
		api.HandleFunc(rt.Pattern, h)
	}

	mux.Handle("/api/", s.deps.Auth.RequireSession(api))
	return mux
}

// isAPIPattern 判断 pattern 是否落在 /api/ 前缀下。
func isAPIPattern(pattern string) bool {
	i := strings.IndexByte(pattern, ' ')
	if i < 0 {
		return strings.HasPrefix(pattern, "/api/")
	}
	return strings.HasPrefix(pattern[i+1:], "/api/")
}
