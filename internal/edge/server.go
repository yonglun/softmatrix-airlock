package edge

import (
	"encoding/json"
	"net/http"

	"github.com/softmatrix/airlock/internal/apikey"
	"github.com/softmatrix/airlock/internal/pricing"
	"github.com/softmatrix/airlock/internal/usage"
)

// Deps 是 Edge 服务器的全部外部依赖，均为接口，便于测试替换。
type Deps struct {
	Keys            apikey.Store
	Prices          pricing.Table
	Usage           usage.Writer
	UpstreamBaseURL string
}

type Server struct {
	deps Deps
}

func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

// Handler 返回装配好的路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	proxy := NewProxy(s.deps.UpstreamBaseURL, s.deps.Prices, s.deps.Usage)
	authenticated := Authenticate(s.deps.Keys)(proxy)

	// /v1/ 下的一切都透明转发给上游，Edge 不维护端点白名单——
	// LiteLLM 新增端点时无需改 Edge。
	mux.Handle("/v1/", authenticated)

	return mux
}
