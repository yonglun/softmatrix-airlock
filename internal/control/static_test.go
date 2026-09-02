package control

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func consoleFS() fs.FS {
	return fstest.MapFS{
		"index.html":         &fstest.MapFile{Data: []byte("<html>console</html>")},
		"assets/app.js":      &fstest.MapFile{Data: []byte("console.log(1)")},
		"platform/orgs.html": &fstest.MapFile{Data: []byte("<html>orgs page</html>")},
	}
}

func TestConsoleServesIndexAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	ConsoleHandler(consoleFS())(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "console")
}

func TestConsoleServesRealAsset(t *testing.T) {
	rec := httptest.NewRecorder()
	ConsoleHandler(consoleFS())(rec, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "console.log")
}

func TestConsoleDeepLinkFallsBackToIndex(t *testing.T) {
	// 客户端路由的深链硬刷新会真的打到服务端。若没有对应页面产物，
	// 必须回 index.html，否则用户刷新一下就 404。
	rec := httptest.NewRecorder()
	ConsoleHandler(consoleFS())(rec, httptest.NewRequest(http.MethodGet, "/finops/approvals", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "console")
}

func TestConsoleDeepLinkServesMatchingPageNotRootIndex(t *testing.T) {
	// next export（无 trailingSlash）把 /platform/orgs 产出成
	// platform/orgs.html，不是 platform/orgs/index.html 也不是
	// 一个叫 platform/orgs 的文件。硬刷新 /platform/orgs 时必须发
	// 这个 .html，而不是悄悄回落成根页面——否则 URL 显示的是
	// /platform/orgs，跑起来的却是根页面的组件，产生错误的落点跳转。
	rec := httptest.NewRecorder()
	ConsoleHandler(consoleFS())(rec, httptest.NewRequest(http.MethodGet, "/platform/orgs", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "orgs page")
}

func TestConsoleWithoutIndexGivesActionableMessage(t *testing.T) {
	// dist 没构建时给一句能照做的提示，而不是让人对着裸 404 排查。
	rec := httptest.NewRecorder()
	ConsoleHandler(fstest.MapFS{".gitkeep": &fstest.MapFile{}})(
		rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "make console")
}

func TestAPINotFoundReturnsJSONShape(t *testing.T) {
	rec := httptest.NewRecorder()
	APINotFoundHandler()(rec, httptest.NewRequest(http.MethodGet, "/api/typo", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Contains(t, rec.Body.String(), "not_found")
}
