package control

import (
	"io/fs"
	"net/http"
	"strings"
)

// ConsoleHandler 服务嵌入的控制台静态站，并为客户端路由做兜底。
//
// 命中真实文件就发文件；命中 next export 产出的 "<路由>.html"（无
// trailingSlash 时的默认命名，如 /platform/orgs 对应 platform/orgs.html）
// 就发那个页面；否则回 index.html——客户端路由的深链硬刷新会真的打到
// 服务端，不兜底就是 404。这三档必须按顺序试：曾经漏了中间那档，
// 深链硬刷新会静默地把「URL 是 /platform/orgs」和「实际跑起来的是根
// 页面组件」这两件事叠在一起，看着像 200 却落错了地方。
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
			if servable(fsys, p) {
				files.ServeHTTP(w, r)
				return
			}
			if servable(fsys, p+".html") {
				r2 := r.Clone(r.Context())
				r2.URL.Path = "/" + p + ".html"
				files.ServeHTTP(w, r2)
				return
			}
		}

		// 不是真实文件、也没有对应的页面产物 → 交给客户端路由，回 index.html。
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	}
}

// servable 判断 fsys 里是否存在这个路径对应的、可以直接发送的文件。
//
// stat 出错（不存在，或底层 fs 故障）时统一当作「不能发」处理、落到
// 兜底逻辑，不在这里分情况报错——兜底路径最终还会再 stat 一次
// index.html，同样的底层故障会在那里暴露，不需要在每一档判断里
// 都重复处理一遍。
func servable(fsys fs.FS, p string) bool {
	info, err := fs.Stat(fsys, p)
	if err != nil {
		return false
	}
	return !info.IsDir()
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
