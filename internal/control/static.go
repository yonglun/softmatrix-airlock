package control

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

// ConsoleHandler 服务嵌入的控制台静态站，并为客户端路由做兜底。
//
// 命中真实文件就发文件，否则回 index.html——客户端路由的深链（如
// /platform/orgs）硬刷新时会真的打到服务端，不兜底就是 404。
//
// 它在路由表里的 pattern 必须是方法无关的 "/"，不能写成 "GET /"：
// 后者与 Handler() 里既有的 "/api/" 互相重叠又互不包含，ServeMux 会在
// 注册时直接 panic（GET / matches fewer methods than /api/, but has a
// more general path pattern）。
func ConsoleHandler(fsys fs.FS) http.HandlerFunc {
	files := http.FileServerFS(fsys)
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			// 二进制里没有构建产物。给一句能照做的提示，
			// 而不是让人对着一个裸 404 去猜。
			http.Error(w,
				"控制台尚未构建：请先执行 make console，再重新构建二进制",
				http.StatusServiceUnavailable)
			return
		}

		if p := strings.TrimPrefix(r.URL.Path, "/"); p != "" {
			_, err := fs.Stat(fsys, p)
			switch {
			case err == nil:
				files.ServeHTTP(w, r)
				return
			case !errors.Is(err, fs.ErrNotExist):
				http.Error(w, "读取静态资源失败", http.StatusInternalServerError)
				return
			}
		}

		// 不是真实文件 → 交给客户端路由，回 index.html。
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	}
}

// APINotFoundHandler 给未匹配的 /api/ 路径返回统一形状的 JSON 404。
//
// 没有它的话，内层 mux 会回 Go 默认的 text/plain "404 page not found"，
// 前端按 {"error":{...}} 解析会拿到意外结构。它也接住用错方法的请求
// （如 DELETE /api/orgs），因为那同样匹配不到具体路由。
func APINotFoundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在")
	}
}
