package web

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testVersion 测试用的版本号（走 New(version) 注入登录页）。
// 刻意不用 "dev"：那与空值回退后的默认展示同形，会让「注入值确实进了模板」
// 与「回退分支生效」两种情形无法区分。
const testVersion = "v9.9.9-test"

// newTestRouter 复刻 main.go 的路由装配：路由表本身来自 h.Mount（不再各处复刻），
// 根路径 "/" 的跳转与 404 由 main.go 提供，这里补一份等价实现以便测试未知路径。
func newTestRouter(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()
	h, err := New(testVersion)
	if err != nil {
		t.Fatalf("New() 失败: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		writeJSONError(w, http.StatusNotFound, "页面不存在")
	})
	h.Mount(mux)
	return h, mux
}

// TestBrandLinksToRepo 品牌元素（登录页标题 / 顶栏 / 侧栏 logo）须链接项目仓库并以新标签页打开。
// 回归：品牌名与仓库地址原先硬编码于多处模板，改名/换仓库需逐处修改且无编译期提示，
// 故按 href 与品牌文案计数，校验模板确实使用了注入的 Brand / RepoURL。
func TestBrandLinksToRepo(t *testing.T) {
	h, _ := newTestRouter(t)
	for _, p := range h.Pages() {
		html := string(p.HTML)
		// 仓库地址本身含 “Qoder-2API-Go”，不能作为旧品牌名残留的证据
		if n := strings.Count(html, `href="`+RepoURL+`" target="_blank" rel="noreferrer"`); n != 3 {
			t.Errorf("%s: 指向仓库的新标签页链接数 = %d，期望 3（登录页标题 + 顶栏 + 侧栏）", p.Key, n)
		}
		if n := strings.Count(html, ">"+Brand+"</a>"); n != 3 {
			t.Errorf("%s: 品牌文案 %q 出现次数 = %d，期望 3（应为 Brand 常量注入，不是写死的字面量）", p.Key, Brand, n)
		}
		// 先移除仓库地址再检索旧品牌，避免 URL 中的 “Qoder-2API-Go” 被误判为残留
		if s := strings.ReplaceAll(html, RepoURL, ""); strings.Contains(s, "Buddy2API") || strings.Contains(s, "Buddy 2API") {
			t.Errorf("%s: 残留参考项目品牌名（应为 %q）", p.Key, Brand)
		}
		if !strings.Contains(html, `<title>`+Brand+` · `) {
			t.Errorf("%s: <title> 未以 %q 开头", p.Key, Brand)
		}
	}
}

// TestVersionInjected 登录页版本号须来自 New(version) 的注入值。
//
// 回归：登录页只发 /admin/api/auth 一个请求，故版本必须在预渲染时就写进 HTML；
// 若注入点在后续重构中被弄丢，登录页会静默退回不显示版本（构建期无从发现）。
// 每一页都含登录页模板，因此所有页面都应带上版本串。
func TestVersionInjected(t *testing.T) {
	h, _ := newTestRouter(t)
	want := `<p class="login-ver"><span>` + testVersion + `</span></p>`
	for _, p := range h.Pages() {
		if !strings.Contains(string(p.HTML), want) {
			t.Errorf("%s: 缺少注入的版本号标记 %q", p.Key, want)
		}
	}
}

// TestVersionDisplay 版本号归一：空值回退 dev，非空补 v 前缀，已是 v / dev 则原样。
func TestVersionDisplay(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "dev"},
		{"   ", "dev"},
		{"dev", "dev"},
		{"v0.1.8", "v0.1.8"},
		{"0.1.8", "v0.1.8"},
		{"main", "vmain"},
	} {
		if got := VersionDisplay(tc.in); got != tc.want {
			t.Errorf("VersionDisplay(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestLoginPageStructure 登录页结构约束：品牌标题为 p（表单标题是唯一 h1）、无遗留分割线节点。
func TestLoginPageStructure(t *testing.T) {
	h, _ := newTestRouter(t)
	for _, p := range h.Pages() {
		html := string(p.HTML)
		if strings.Contains(html, `class="login-divider"`) {
			t.Errorf("%s: 残留 login-divider 节点（分割线应由 .login-brand::after 绘制）", p.Key)
		}
		// 登录页只有一个 <h1>（表单标题）；品牌标题降为 <p>，否则一页两个 h1 破坏大纲。
		// 注意不能断言整份文档 h1 总数：文档内常驻全部 5 个页面，各自另有一个 h1。
		if n := strings.Count(html, `<h1 class="login-h1">`); n != 1 {
			t.Errorf("%s: 登录页 h1 数 = %d，期望 1（仅表单标题「登录」）", p.Key, n)
		}
		if !strings.Contains(html, `<p class="login-brand-title">`) {
			t.Errorf("%s: 品牌标题应为 <p class=\"login-brand-title\">", p.Key)
		}
	}
}

// TestPagesRendered 每页都应渲染出完整 HTML：含 data-page、标题、全部 view 容器。
//
// 文档内常驻全部页面（软导航只切 .view 显隐，不重载文档），因此每份 HTML 都含全部
// <section class="space">；各页 <title> / data-page 仍然各自正确，深链与刷新不受影响。
func TestPagesRendered(t *testing.T) {
	h, _ := newTestRouter(t)
	if got, want := len(h.Keys()), 5; got != want {
		t.Fatalf("页面数量 = %d，期望 %d", got, want)
	}
	for _, p := range h.Pages() {
		html := string(p.HTML)
		for _, want := range []string{
			`<body data-page="` + p.Key + `">`,
			`href="/admin/assets/app.css"`,
			`src="/admin/assets/app.js"`,
			`src="/admin/assets/alpine.js"`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("%s: 缺少 %q", p.Key, want)
			}
		}
		// 服务端渲染的标题只作为首帧标题（软导航后由 app.js 的 applyTitle 接管），
		// 故此处用正则匹配不带外部分组、与具体分隔符无关的写法。
		titleRe := regexp.MustCompile(`<title>[^<]*` + regexp.QuoteMeta(p.Title) + `</title>`)
		if !titleRe.MatchString(html) {
			t.Errorf("%s: <title> 未包含页名 %q", p.Key, p.Title)
		}
		// 每个 view 容器必须带 x-cloak：未激活的 view 在 Alpine 接管前不能闪现
		for _, k := range h.Keys() {
			marker := `<div class="view" x-cloak x-show="view==='` + k + `'">`
			if !strings.Contains(html, marker) {
				t.Errorf("%s: 缺少视图容器 %s", p.Key, marker)
			}
		}
		// 全部页面 section 常驻同一文档
		if n := strings.Count(html, `<section x-cloak class="space">`); n != len(h.Keys()) {
			t.Errorf("%s: 页面 section 数量 = %d，期望 %d", p.Key, n, len(h.Keys()))
		}
	}
}

// TestNavBinding 侧边栏导航：每页共用同一份，链接数等于页数，逐项校验 active 绑定与点击拦截。
// active 由 Alpine 按 view 绑定（服务端写死的高亮无法随软导航切换）；
// href 仍须为真实路径（无 JS / 新标签页可用），且 JS 侧由 href 推导页面 key。
func TestNavBinding(t *testing.T) {
	h, _ := newTestRouter(t)
	for _, p := range h.Pages() {
		html := string(p.HTML)
		if n := strings.Count(html, `<a class="nav-link" :class="{active:view===`); n != len(h.Keys()) {
			t.Errorf("%s: 导航链接数 = %d，期望 %d", p.Key, n, len(h.Keys()))
		}
		if strings.Contains(html, `class="nav-link active"`) {
			t.Errorf("%s: active 态不应由服务端写死", p.Key)
		}
		for i := range pages {
			href := "/admin/" + pages[i].key
			if i == 0 {
				href = "/admin" // 统计页走命名空间根
			}
			for _, want := range []string{
				`:class="{active:view==='` + pages[i].key + `'}"`,
				`href="` + href + `"`,
				`@click="navClick($event)"`,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("%s: 导航缺少 %s", p.Key, want)
				}
			}
		}
	}
}

// TestPageKeyValidation 页面 key 会进入 URL、文件名与 Alpine 表达式字面量，字符集必须保守。
func TestPageKeyValidation(t *testing.T) {
	for _, k := range []string{"stats", "a", "keys", "growth2", "growth-travel", "growth_travel"} {
		if !validPageKey(k) {
			t.Errorf("validPageKey(%q) = false，期望 true", k)
		}
	}
	for _, k := range []string{"", "Stats", "1a", "a/b", "../etc", "a b", `a'b`, `a"b`, "a<b"} {
		if validPageKey(k) {
			t.Errorf("validPageKey(%q) = true，期望 false", k)
		}
	}
}

// TestScriptOrder app.js 必须排在 alpine.js 之前。
//
// alpine.min.js 末尾是 queueMicrotask(()=>Alpine.start())，微任务会在下一个 defer 脚本
// 执行前清空：若 Alpine 先加载，就会在 app() 定义前启动（控制台报 app is not defined、整页不渲染）。
func TestScriptOrder(t *testing.T) {
	h, _ := newTestRouter(t)
	html := string(h.pages["stats"].HTML)
	iApp := strings.Index(html, "/admin/assets/app.js")
	iAlpine := strings.Index(html, "/admin/assets/alpine.js")
	if iApp < 0 || iAlpine < 0 {
		t.Fatalf("未找到脚本标签：app.js=%d alpine.js=%d", iApp, iAlpine)
	}
	if iApp > iAlpine {
		t.Error("app.js 必须排在 alpine.js 之前（否则 Alpine 会在 app() 定义前自启动）")
	}
}

// TestMountRoutes 页面路由 / 规范跳转 / 静态资源 / 405 / 404 行为；一律经真实 mux
// 发请求，直接调 h.PageHandler 会绕过路由层 method 分派（HEAD/405 仅在路由层成立）。
func TestMountRoutes(t *testing.T) {
	_, mux := newTestRouter(t)

	cases := []struct {
		method, path string
		code         int
		location     string
	}{
		{http.MethodGet, "/admin", http.StatusOK, ""},
		{http.MethodGet, "/admin/keys", http.StatusOK, ""},
		{http.MethodGet, "/admin/token", http.StatusOK, ""},
		// 模型页已移除：模型列表迁入统计页，旧 URL 不再有页面（落到根处理器的 404）
		{http.MethodGet, "/admin/models", http.StatusNotFound, ""},
		{http.MethodGet, "/admin/settings", http.StatusOK, ""},
		// 尾斜杠与首页别名：308 到规范 URL，保证每个页面只有一个规范路径
		{http.MethodGet, "/admin/", http.StatusPermanentRedirect, "/admin"},
		{http.MethodGet, "/admin/keys/", http.StatusPermanentRedirect, "/admin/keys"},
		{http.MethodGet, "/admin/settings/", http.StatusPermanentRedirect, "/admin/settings"},
		{http.MethodGet, "/admin/stats", http.StatusPermanentRedirect, "/admin"},  // 规范化为 /admin（308 永久）
		{http.MethodGet, "/admin/stats/", http.StatusPermanentRedirect, "/admin"}, // 尾斜杠同样跳转
		{http.MethodGet, "/admin/assets/app.js", http.StatusOK, ""},
		{http.MethodGet, "/admin/assets/app.css", http.StatusOK, ""},
		{http.MethodGet, "/admin/assets/alpine.js", http.StatusOK, ""},
		{http.MethodGet, "/admin/assets/nope.js", http.StatusNotFound, ""},
		{http.MethodGet, "/admin/assets/pages/keys.js", http.StatusNotFound, ""}, // 页面脚本不单独对外
		{http.MethodGet, "/nope", http.StatusNotFound, ""},
		{http.MethodPost, "/admin/assets/app.js", http.StatusMethodNotAllowed, ""},
		{http.MethodDelete, "/admin/assets/app.css", http.StatusMethodNotAllowed, ""},
		// 页面同样只接受 GET/HEAD：其他方法返回统一 JSON 405，而非标准库的空 body
		{http.MethodPost, "/admin", http.StatusMethodNotAllowed, ""},
		{http.MethodPost, "/admin/keys", http.StatusMethodNotAllowed, ""},
		// /admin/keys/ 为别名：先 308 跳到 /admin/keys（308 保留方法），到终点后由 pageMethodGuard 判方法
		{http.MethodPost, "/admin/keys/", http.StatusPermanentRedirect, "/admin/keys"},
		{http.MethodDelete, "/admin/settings", http.StatusMethodNotAllowed, ""},
		// HEAD 须与 GET 同等待遇（链接检查器 / 预览抓取 / 代理预检均发 HEAD）
		{http.MethodHead, "/admin", http.StatusOK, ""},
		{http.MethodHead, "/admin/keys", http.StatusOK, ""},
		{http.MethodHead, "/admin/keys/", http.StatusPermanentRedirect, "/admin/keys"},
		{http.MethodHead, "/admin/stats", http.StatusPermanentRedirect, "/admin"},
		{http.MethodHead, "/nope", http.StatusNotFound, ""},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s %s = %d，期望 %d", c.method, c.path, rec.Code, c.code)
		}
		if c.location != "" && rec.Header().Get("Location") != c.location {
			t.Errorf("%s %s Location = %q，期望 %q", c.method, c.path, rec.Header().Get("Location"), c.location)
		}
	}
}

// TestHeadViaRouter HEAD 经真实路由须返回 200、空 body 与 Content-Length。
//
// 回归：页面处理器自查 method（而非 ServeMux 的方法前缀模式）时，HEAD 复用 GET 的
// 处理链由 http.ServeContent 丢弃 body，但必须仍带 Content-Length。
func TestHeadViaRouter(t *testing.T) {
	_, mux := newTestRouter(t)
	for _, path := range []string{"/admin", "/admin/keys", "/admin/settings", "/admin/assets/app.js", "/admin/assets/app.css", "/admin/assets/alpine.js"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("HEAD %s = %d，期望 200", path, rec.Code)
			continue
		}
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD %s 返回了 %d 字节 body", path, rec.Body.Len())
		}
		if rec.Header().Get("Content-Length") == "" {
			t.Errorf("HEAD %s 缺少 Content-Length", path)
		}
	}
}

// TestPageMethodNotAllowedBody 页面路由的 405 由 pageMethodGuard 返回统一 JSON 错误体，Allow 为 GET, HEAD。
func TestPageMethodNotAllowedBody(t *testing.T) {
	_, mux := newTestRouter(t)
	for _, path := range []string{"/admin", "/admin/keys", "/admin/settings"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d，期望 405", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("POST %s Allow = %q，期望 GET, HEAD", path, got)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("POST %s Content-Type = %q，期望 JSON", path, ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"error"`) {
			t.Errorf("POST %s body = %q", path, body)
		}
	}
}

// TestMethodNotAllowedBody 静态资源处理器直接返回的 405 同样使用统一 JSON 错误体与 Allow。
func TestMethodNotAllowedBody(t *testing.T) {
	h, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	h.AssetHandler(rec, httptest.NewRequest(http.MethodPost, "/admin/assets/app.js", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /admin/assets/app.js = %d，期望 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q，期望 GET, HEAD", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("405 Content-Type = %q，期望 JSON", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error"`) {
		t.Errorf("405 body = %q", body)
	}
}

// TestPageHandlerUnknownKey 未注册的页面 key 不能 panic、不能返回空 200。
func TestPageHandlerUnknownKey(t *testing.T) {
	h, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	h.PageHandler("nope")(rec, httptest.NewRequest(http.MethodGet, "/admin/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未注册页面 = %d，期望 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("404 body = %q", rec.Body.String())
	}
}

// TestNoSniff 页面与静态资源显式设置 X-Content-Type-Options: nosniff（处理器自带，
// 不再依赖 main.go 的全局中间件——标准库 mux 无中间件层）。
func TestNoSniff(t *testing.T) {
	_, mux := newTestRouter(t)
	for _, path := range []string{"/admin", "/admin/keys", "/admin/assets/app.js", "/admin/assets/app.css"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q，期望 nosniff", path, got)
		}
	}
}

// TestAssetCaching 资源缓存语义：页面 no-cache、Alpine 长缓存、其余带内容哈希 Etag，
// 并校验 Content-Type（写错会直接导致样式/脚本失效）。
func TestAssetCaching(t *testing.T) {
	h, _ := newTestRouter(t)

	// 页面：no-cache + Etag + Content-Length，条件请求 → 304
	for _, key := range h.Keys() {
		path := "/admin"
		if key != h.Keys()[0] {
			path = "/admin/" + key
		}
		rec := httptest.NewRecorder()
		h.PageHandler(key)(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q，期望 no-cache", key, got)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("%s: Content-Type = %q", key, got)
		}
		if got := rec.Header().Get("Content-Length"); got == "" {
			t.Errorf("%s: 缺少 Content-Length", key)
		}
		etag := rec.Header().Get("Etag")
		if etag == "" {
			t.Errorf("%s: 缺少 Etag", key)
			continue
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", etag)
		rec = httptest.NewRecorder()
		h.PageHandler(key)(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("%s: 条件请求 = %d，期望 304", key, rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: 304 不应带 body，实际 %d 字节", key, rec.Body.Len())
		}
	}

	// Alpine：沿用 1 天缓存（体积大、几乎不变）
	rec := httptest.NewRecorder()
	h.AssetHandler(rec, httptest.NewRequest(http.MethodGet, "/admin/assets/alpine.js", nil))
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("alpine.js Cache-Control = %q，期望 public, max-age=86400", got)
	}

	// 其余资源：no-cache + Etag，且条件请求返回 304
	types := map[string]string{
		"/admin/assets/app.js":  "application/javascript; charset=utf-8",
		"/admin/assets/app.css": "text/css; charset=utf-8",
	}
	for path, ctype := range types {
		rec := httptest.NewRecorder()
		h.AssetHandler(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Content-Type"); got != ctype {
			t.Errorf("%s: Content-Type = %q，期望 %q", path, got, ctype)
		}
		etag := rec.Header().Get("Etag")
		if etag == "" {
			t.Errorf("%s: 缺少 Etag", path)
			continue
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", etag)
		rec = httptest.NewRecorder()
		h.AssetHandler(rec, req)
		if rec.Code != http.StatusNotModified {
			t.Errorf("%s: 条件请求 = %d，期望 304", path, rec.Code)
		}
	}
}

// TestHeadRequests HEAD 不返回 body 且带 Content-Length（处理器层）；仅覆盖 http.ServeContent
// 的行为，路由层 HEAD 由 TestHeadViaRouter 覆盖。
func TestHeadRequests(t *testing.T) {
	h, _ := newTestRouter(t)
	rec := httptest.NewRecorder()
	h.PageHandler("keys")(rec, httptest.NewRequest(http.MethodHead, "/admin/keys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /admin/keys = %d，期望 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD /admin/keys 返回了 %d 字节 body", rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") == "" {
		t.Error("HEAD /admin/keys 缺少 Content-Length")
	}
	rec = httptest.NewRecorder()
	h.AssetHandler(rec, httptest.NewRequest(http.MethodHead, "/admin/assets/app.js", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD /admin/assets/app.js = %d，body %d 字节", rec.Code, rec.Body.Len())
	}
}

// TestAppMergesAllPages 总装必须合并全部页面层。
//
// 5 个 view 常驻同一文档，Alpine 会在 x-data 根作用域（即 app() 返回的实例）上求值
// 全部页面的指令；若只合并当前页，其他页的表达式会报 ReferenceError 且视图渲染不全。
func TestAppMergesAllPages(t *testing.T) {
	h, _ := newTestRouter(t)
	js := string(h.assets["/admin/assets/app.js"])
	if !strings.Contains(js, "for(const id of Object.keys(PAGES))") {
		t.Error("app() 必须遍历全部 PAGES 合并页面层（否则常驻 view 的表达式会 ReferenceError）")
	}
	// 导航必须在 Alpine 求值中执行：在 app() 里直接给原始对象赋值不会触发重渲染
	if strings.Contains(js, "window.addEventListener('popstate'") {
		t.Error("popstate 应由 Alpine 绑定（@popstate.window）转发，直接监听会拿到非响应式 this")
	}
	shell := string(h.pages["stats"].HTML)
	if !strings.Contains(shell, `@popstate.window="onPop($event)"`) {
		t.Error("shell 缺少 popstate 绑定")
	}
}

// TestPageJSRegistered 每个页面都必须有对应的页面层脚本，且注册 key 与页面 key 一致。
func TestPageJSRegistered(t *testing.T) {
	h, _ := newTestRouter(t)
	js := string(h.assets["/admin/assets/app.js"])
	if !strings.Contains(js, "function app()") {
		t.Fatal("app.js 缺少总装函数 app()")
	}
	// 共享层必须先于页面层注册（否则 PAGE 未定义）
	iRegistry := strings.Index(js, "function PAGE(")
	if iRegistry < 0 {
		t.Fatal("app.js 缺少 PAGE 注册表")
	}
	for _, k := range h.Keys() {
		marker := "PAGE('" + k + "', {"
		i := strings.Index(js, marker)
		if i < 0 {
			t.Errorf("app.js 缺少页面层注册 %s", marker)
			continue
		}
		if i < iRegistry {
			t.Errorf("%s 的注册早于 PAGE 注册表定义", k)
		}
	}
}

// TestEmbedShape 内嵌文件清单稳定：shell + 每页 HTML + 每页脚本。
func TestEmbedShape(t *testing.T) {
	if _, err := fs.ReadFile(pageFS, "shell.html"); err != nil {
		t.Fatalf("读取 shell.html: %v", err)
	}
	for _, p := range pages {
		if _, err := fs.ReadFile(pageFS, "pages/"+p.key+".html"); err != nil {
			t.Errorf("缺少 pages/%s.html: %v", p.key, err)
		}
		if _, err := fs.ReadFile(assetFS, "assets/pages/"+p.key+".js"); err != nil {
			t.Errorf("缺少 assets/pages/%s.js: %v", p.key, err)
		}
	}
}

// TestMovedModelsAndTier 面板结构调整的回归：模型列表迁入统计页（点击复制语义不变），账号
// 等级徽标移到令牌页，令牌页提供跳转 Qoder 集成页的创建令牌链接。断言基于页面源文件——
// 每份文档都内嵌全部 view，对渲染产物断言「不存在」定位不到具体页面。
func TestMovedModelsAndTier(t *testing.T) {
	read := func(fsys fs.FS, name string) string {
		t.Helper()
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		return string(b)
	}

	// 模型页确实已移除（页面与页面层脚本都不再内嵌；/admin/models 的 404 见 TestMountRoutes）
	for _, f := range []struct {
		fsys fs.FS
		name string
	}{{pageFS, "pages/models.html"}, {assetFS, "assets/pages/models.js"}} {
		if _, err := fs.ReadFile(f.fsys, f.name); err == nil {
			t.Errorf("%s 应已移除（模型列表迁入统计页）", f.name)
		}
	}

	stats := read(pageFS, "pages/stats.html")
	for _, want := range []string{
		`<h3>模型列表</h3>`,
		`x-text="models.models.length + ' 个模型'"`,
		`@click="copy(m)"`, // 点击卡片复制模型名的既有行为
	} {
		if !strings.Contains(stats, want) {
			t.Errorf("统计页缺少迁入的模型列表元素 %s", want)
		}
	}
	if strings.Contains(stats, `x-text="accountTag"`) {
		t.Error("统计页不应再显示账号等级徽标（已移至令牌页）")
	}

	token := read(pageFS, "pages/token.html")
	if !strings.Contains(token, `x-text="accountTag"`) {
		t.Error("令牌页缺少账号等级徽标")
	}
	if !strings.Contains(token, `href="https://qoder.cn/account/integrations"`) {
		t.Error("令牌页缺少跳转 Qoder 集成页创建令牌的链接")
	}

	// 令牌页要渲染等级徽标，其 load() 必须拉取 stats 快照（徽标数据源）
	tokenJS := read(assetFS, "assets/pages/token.js")
	if !strings.Contains(tokenJS, "this.loadStats()") {
		t.Error("令牌页 load() 必须加载 stats 快照（等级徽标的数据源）")
	}
	// 模型列表迁入统计页后，其加载动作随之迁到 stats 页面层
	statsJS := read(assetFS, "assets/pages/stats.js")
	if !strings.Contains(statsJS, "this.loadModels()") {
		t.Error("统计页 load() 必须加载模型列表（模型页已移除）")
	}
}

// TestNoTemplateLeak 渲染产物不得残留模板指令。
func TestNoTemplateLeak(t *testing.T) {
	h, _ := newTestRouter(t)
	for _, p := range h.Pages() {
		html := string(p.HTML)
		if strings.Contains(html, "{{") || strings.Contains(html, "}}") {
			t.Errorf("%s: 渲染产物残留模板指令", p.Key)
		}
	}
}

// ═══ 额度池展示回归（自 admin 包迁移：面板 UI 已移入 web 包） ═══

// statsPageJS 取拼接产物中 stats 页面层脚本的源码段。
func statsPageJS(t *testing.T) string {
	t.Helper()
	h, _ := newTestRouter(t)
	js := string(h.assets["/admin/assets/app.js"])
	start := strings.Index(js, "PAGE('stats', {")
	if start < 0 {
		t.Fatal("stats 页面层未注册")
	}
	end := len(js)
	if next := strings.Index(js[start:], "\nPAGE('"); next > 0 {
		end = start + next
	}
	return js[start:end]
}

// The panel's headline must reflect the allowance the account actually holds.
// A free account reports userQuota.total=0 while the usable budget sits in
// addOnQuota; keying the headline off userQuota rendered "套餐 8 / 0 · 剩 0"
// for an account that still had credits, and hid the pool that really mattered.
func TestQuotaSourceSelectionIgnoresEmptyPools(t *testing.T) {
	src := statsPageJS(t)

	// A zero-total pool carries no allowance and must not be offered as one.
	for _, guard := range []string{
		"acct.user_quota && Number(acct.user_quota.total || 0) > 0",
		"acct.add_on_quota && Number(acct.add_on_quota.total || 0) > 0",
		"acct.org_resource_package && Number(acct.org_resource_package.cap || 0) > 0",
	} {
		if !strings.Contains(src, guard) {
			t.Errorf("额度池选择必须跳过零容量池；缺少守卫 %q", guard)
		}
	}

	// 首行与明细卡必须共用同一池选择（quotaPoolsList），否则两处可能不一致。
	if !strings.Contains(src, "get creditsHeadline()") || !strings.Contains(src, "quotaPoolsList") {
		t.Error("creditsHeadline 必须经 quotaPoolsList 选择池")
	}
	if strings.Contains(src, "acct.user_quota ||") {
		t.Error("creditsHeadline 不得硬编码 user_quota 作为首行数据源")
	}
}

// The 合计剩余 summary must not absorb credits the account cannot spend: an
// unavailable org_resource_package still gets a row marked 不可用, so counting
// its remaining credits made the summary contradict the row right above it.
func TestQuotaSummaryOnlyCountsAvailablePools(t *testing.T) {
	src := statsPageJS(t)

	guard := "if (pool.available) totalRemaining += pool.remaining;"
	if !strings.Contains(src, guard) {
		t.Errorf("合计剩余只累计可花费的额度；缺少守卫 %q", guard)
	}
	// Any other accumulation must carry the same availability guard.
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "totalRemaining += ") && !strings.Contains(line, "pool.available") {
			t.Errorf("totalRemaining 未带可用性守卫即累计：%q", strings.TrimSpace(line))
		}
	}
}

// The stats page must show what this service itself was charged, apart from the
// account-wide pools (which include IDE/CLI traffic). The average divides by
// billed_requests, not the request counter, which holds failures and uncharged
// calls too.
func TestStatsPageShowsActualCreditSpend(t *testing.T) {
	src := statsPageJS(t)
	for _, want := range []string{
		"get totalCredits()", "get cycleCredits()", "get billedCount()", "get avgCredits()",
		"get shownCredits()", "get creditsDetail()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("本服务实际消耗需要 %s", want)
		}
	}
	// 计数与均值必须取 billed_requests / avg_credits（服务端已按计费帧算好），
	// 不能拿 total（含失败、未计费、无 usage 帧）当分母。
	if !strings.Contains(src, "(this.stats||{}).billed_requests") {
		t.Error("billedCount 必须读 billed_requests")
	}
	if !strings.Contains(src, "(this.stats||{}).avg_credits") {
		t.Error("avgCredits 必须读服务端的 avg_credits")
	}

	h, _ := newTestRouter(t)
	html := string(h.pages["stats"].HTML)
	if !strings.Contains(html, "实际消耗 Credits") {
		t.Error("统计页缺少「实际消耗 Credits」卡片")
	}
	if !strings.Contains(html, `fmtCredits(shownCredits)`) {
		t.Error("实际消耗卡片必须以 shownCredits 渲染主体数字")
	}
	// 模型表必须分别展示消耗与平均：只有总量无法定位是哪个模型在花钱。
	for _, want := range []string{`fmtCredits(m.credits)`, `fmtCredits(m.avg_credits)`} {
		if !strings.Contains(html, want) {
			t.Errorf("按模型统计缺少消耗列 %s", want)
		}
	}
}

// A freshly rolled cycle legitimately holds 0 credits, so the card must pick the
// cycle figure by the presence of a boundary, not by a non-zero total:
// `cycle > 0 ? cycle : lifetime` would read as if the reset never happened.
func TestStatsPageCycleSelectionDoesNotFallBackOnZero(t *testing.T) {
	src := statsPageJS(t)
	guard := "return this.hasCycle ? this.cycleCredits : this.totalCredits;"
	if !strings.Contains(src, guard) {
		t.Errorf("实际消耗必须是按周期边界选择口径；缺少 %q", guard)
	}
	for _, dead := range []string{"cycle_credits > 0 ?", "cycle > 0 ?"} {
		if strings.Contains(src, dead) {
			t.Errorf("零消耗周期不得回退到累计值（死分支 %q）", dead)
		}
	}
}

// An older build's snapshot carries credits but no billed count, so the card
// must name the missing metric instead of reading "暂无计费请求" directly above
// a non-zero total.
func TestStatsPageExplainsLegacyDataWithoutBilledCount(t *testing.T) {
	src := statsPageJS(t)
	if !strings.Contains(src, "this.totalCredits > 0 ? '计费次数与均值自本版起统计'") {
		t.Error("升级前的数据（只有 credits、无计费次数）必须说明口径，不得报「暂无计费请求」")
	}
}

// fmtCredits never returns a negative string and total comes from
// Number(pool.quota[...] || 0), so a '—' fallback branch for the total cell
// would be dead code. The cell must render fmtCredits(p.total) directly
// （新面板中该单元格在 Alpine 模板 x-text 绑定里，不在 JS 拼接的 HTML 里）。
func TestQuotaDetailsHasNoDeadTotalBranch(t *testing.T) {
	src := statsPageJS(t)
	for _, dead := range []string{"total >= 0 ?", "? fmtCredits(p.total) : '—'"} {
		if strings.Contains(src, dead) {
			t.Errorf("额度明细的 total 死分支 %q 必须移除", dead)
		}
	}
	h, _ := newTestRouter(t)
	html := string(h.pages["stats"].HTML)
	if !strings.Contains(html, `fmtCredits(p.used) + ' / ' + fmtCredits(p.total)`) {
		t.Error("额度明细必须直接以 fmtCredits(p.total) 渲染总额")
	}
}

// harnessJS 前端行为测试 harness：用最小 DOM stub 驱动真实 app.js 与各页脚本，断言导航与
// 「进场加载」语义；这类行为无法在 Go 侧断言（只能对源码文本做 grep）。
const harnessJS = `
(async function () {
const fs = require('fs');
const src = process.argv.slice(2).map(function (f) { return fs.readFileSync(f, 'utf8'); }).join('\n');

const loads = [];
// 侧栏导航 DOM 的替代对象：applyTitle 依据 .nav-link 的 href 与文案建页名映射，
// 置空用于模拟壳层不在 DOM（会话过期、登录页展示中）。
let navLinks = [];
function makeNavLinks() {
  return [['/admin', ' 统计 '], ['/admin/keys', ' 密钥 '], ['/admin/token', ' 令牌 '],
          ['/admin/logs', ' 日志 '], ['/admin/settings', ' 设置 ']].map(function (p) {
    return { getAttribute: function () { return p[0]; }, textContent: p[1] };
  });
}
navLinks = makeNavLinks();
global.localStorage = { getItem: function () { return null; }, setItem: function () {} };
global.document = {
  documentElement: { classList: { toggle: function () {} } },
  body: { dataset: { page: 'stats' } },
  querySelectorAll: function () { return navLinks; },
  title: 'Qoder-2API · 统计',
};
global.matchMedia = function () { return { matches: false, addEventListener: function () {} }; };
global.window = { addEventListener: function () {}, scrollTo: function () {} };
global.history = { state: null, replaceState: function () {}, pushState: function () {} };
global.location = { pathname: '/admin' };
global.requestAnimationFrame = function (fn) { fn(); };
global.setInterval = function () { return 0; };
global.fetch = async function () { return { status: 200, ok: true, json: async function () { return {}; } }; };

// app.js / pages 里的 PAGES、app 都是 eval 作用域内的 const/function，不会泄到本模块，
// 因此在同一次 eval 里把它们取出来（不靠间接 eval 的全局语义）。
const api = eval(src + '\n;({ PAGES: PAGES, app: app });');
const PAGES = api.PAGES;
const inst = api.app();
// 记录两类事实：「页面进场」（enter 调用 PAGES[k].load）与「实际发出的加载动作」：
// 前者验证进场语义（首屏/切页/保活），后者验证 load() 确实发出了请求。
Object.keys(PAGES).forEach(function (k) {
  const orig = PAGES[k].load;
  PAGES[k].load = function () { loads.push('enter:' + k); if (orig) return orig.call(this); };
});
['loadStats', 'loadKeys', 'loadPAT', 'loadModels', 'loadConfig'].forEach(function (m) {
  if (typeof inst[m] === 'function') inst[m] = function () { loads.push(m); };
});
inst.$nextTick = function (fn) { fn(); }; // Alpine magic（onPop 的滚动回位用）

function fail(msg) {
  console.error('FAIL: ' + msg + ' | loads=' + JSON.stringify(loads) +
                ' | view=' + inst.view + ' | entered=' + JSON.stringify(inst.entered));
  process.exit(1);
}
function count(name) { return loads.filter(function (x) { return x === name; }).length; }

// 1) 首屏页必须进场并加载数据（回归：entered 预置首屏 key 会使 load() 被跳过）
inst.afterLogin();
if (count('enter:stats') !== 1) fail('首屏页 stats 应恰好进场一次');
if (count('loadStats') !== 1) fail('首屏页 stats 的 load() 未把数据请求发出去');
if (count('loadModels') !== 1) fail('首屏页 stats 的 load() 未把模型列表请求发出去（模型页已迁入统计页）');

// 2) 切页：目标页进场一次，view 切换，URL 换成 /admin/<key>，移动端侧栏收起
inst.mobileNav = true;
inst.navigate('keys');
if (inst.view !== 'keys') fail('view 未切换到 keys');
if (count('enter:keys') !== 1) fail('切到 keys 后应恰好进场一次');
if (count('loadKeys') < 1) fail('切到 keys 后未加载 keys');
if (inst.mobileNav !== false) fail('navigate() 未收起移动端侧栏');

// 3) 切回已进场的页（5 秒内）：不重复进场（保活语义不能被破坏）
inst.navigate('stats');
if (inst.view !== 'stats') fail('view 未切回 stats');
if (count('enter:stats') !== 1) fail('切回已进场页时重复进场');

// 4) 前进/后退：换页时同样收起侧栏并让未进场页进场
inst.mobileNav = true;
inst.onPop({ state: { view: 'logs' } });
if (inst.view !== 'logs') fail('onPop 未切换 view');
if (count('enter:logs') !== 1) fail('onPop 到未进场页时应进场');
if (inst.mobileNav !== false) fail('onPop 换页时未收起移动端侧栏');

// 5) 页名映射：壳层不在 DOM 时的空采集不得写入缓存（写入后不再更新）
inst._titles = undefined;
navLinks = [];
inst.applyTitle('logs');            // 空采集：不应写入 _titles
navLinks = makeNavLinks();
inst.applyTitle('logs');
if (!/日志$/.test(document.title)) {
  fail('空采集污染了页名映射，标题不再更新: ' + document.title);
}
inst.navigate('settings');         // 常规软导航也要更新标题
if (!/设置$/.test(document.title)) fail('navigate 后标题未更新: ' + document.title);

// 6) 会话边界：401 / 退出后再登录须清空「已进场」，否则当前页保留过期数据
inst.view = 'token';
inst.entered = { token: Date.now() };   // 模拟本次会话已进过 token 且数据已旧
inst.resetSession();                       // 会话边界：清空「已进场」
loads.length = 0;
inst.afterLogin();                         // 重新登录：首屏必须重新进场取新数据
if (count('enter:token') !== 1) fail('重新登录后当前页应重新进场取新数据');

// 7) 会话边界也须由 401 触发，而非仅显式 resetSession
const savedFetch = global.fetch;
global.fetch = async function () { return { status: 401, ok: false, json: async function () { return {}; } }; };
inst.entered = { token: Date.now() };
let threw = false;
try { await inst.api('/admin/api/stats'); } catch (e) { threw = true; }
if (!threw) fail('401 应抛错');
if (inst.loggedIn !== false) fail('401 后 loggedIn 应为 false');
if (Object.keys(inst.entered).length !== 0) fail('401 未清空 entered（旧数据会残留）');

// 7b) 登录端点自身的 401 是「密码错误」而非会话过期：必须保留服务端文案，
// 且不得把页面切回未登录态/清空进场缓存（此刻本就未登录，切态只会吞掉错误提示）
inst.loggedIn = false;
inst.entered = { token: Date.now() };
let loginErr = '';
global.fetch = async function () {
  return { ok: false, status: 401, json: async function () { return { error: '密码错误' }; } };
};
try { await inst.api('/admin/api/login', { method: 'POST' }); } catch (e) { loginErr = e.message; }
if (loginErr !== '密码错误') fail('/admin/api/login 的 401 应报服务端原文「密码错误」，实得: ' + JSON.stringify(loginErr));
if (Object.keys(inst.entered).length === 0) fail('登录端点的 401 不应清空 entered（与 api 通道共用时不得误伤已进过页的缓存）');
global.fetch = savedFetch;

// 8) realtime 页每次进入都刷新，短间隔重复进入退化为保活（本面板各页均按 realtime 处理）
loads.length = 0;
inst.entered = {};
inst.navigate('keys');
if (count('enter:keys') !== 1) fail('keys 首次进入应进场');
inst.navigate('stats');
inst.entered.keys = Date.now() - 60 * 1000;   // 模拟 1 分钟前进过
inst.navigate('keys');
if (count('enter:keys') !== 2) fail('realtime 页（keys）再次进入应刷新');
inst.navigate('stats');
inst.navigate('keys');                        // 刚进过：间隔 < 5s，退化为保活
if (count('enter:keys') !== 2) fail('realtime 页短间隔重复进入不应反复请求');

// 9) 删除密钥走真实页面层代码：确认弹窗 + DELETE /admin/api/keys?id=…（旧面板语义保持）
loads.length = 0;
let deleted = '';
global.fetch = async function (url, opts) {
  if ((opts && opts.method) === 'DELETE') { deleted = url; return { ok: true, status: 200, json: async function () { return { status: 'deleted' }; } }; }
  return { ok: true, status: 200, json: async function () { return { keys: [] }; } };
};
inst.deleteKey({ id: 'k1', key: 'sk-x', note: '', created_at: 0 });
if (!inst.confirmBox.open) fail('删除密钥应先弹确认框');
inst.confirmBox.onOk();                       // 确认：执行删除
await new Promise(function (r) { setTimeout(r, 0); });   // 排空 busy 内的 await 链
if (deleted.indexOf('/admin/api/keys?id=k1') !== 0) fail('删除请求未发往 /admin/api/keys?id=…，实发: ' + deleted);
if (count('loadKeys') < 1) fail('删除成功后应刷新密钥列表');
global.fetch = savedFetch;

console.log('OK ' + JSON.stringify(loads));
})().catch(function (e) { console.error('FAIL: harness 异常: ' + ((e && e.stack) || e)); process.exit(1); });
`

// shadowHarnessJS 检查页面层与共享层成员重名（PAGES 各页 vs appShell 返回值）。
// 只取 appShell() 的返回值与 PAGES 键名做集合运算，无需 DOM stub。
const shadowHarnessJS = `
const fs = require('fs');
const src = process.argv.slice(2).map(function (f) { return fs.readFileSync(f, 'utf8'); }).join('\n');
// appShell() 体内读取 localStorage（theme 初值），故取成员名须实际调用该函数
global.localStorage = { getItem: function () { return null; }, setItem: function () {} };
global.matchMedia = function () { return { matches: false, addEventListener: function () {} }; };
const api = eval(src + '\n;({ PAGES: PAGES, appShell: appShell });');

const shellNames = Object.getOwnPropertyNames(api.appShell());
const shellSet = new Set(shellNames);
const bad = [];
Object.keys(api.PAGES).forEach(function (k) {
  Object.getOwnPropertyNames(api.PAGES[k]).forEach(function (m) {
    if (shellSet.has(m)) bad.push(k + '.' + m);
  });
});
if (bad.length) {
  console.error('FAIL: 页面层成员覆盖了共享层成员（共享层实现会被静默顶掉）: ' + bad.join(', '));
  process.exit(1);
}
console.log('OK 无重名覆盖（共享层 ' + shellNames.length + ' 个成员，页面层 ' + Object.keys(api.PAGES).length + ' 页）');
`

// TestPageLayerDoesNotShadowShell 页面层成员不得与共享层成员重名：app() 按 Object.keys(PAGES)
// 顺序将各页面层 defineProperty 到同一 x-data 根作用域，后注册者覆盖先注册者，同名即覆盖共享层实现。
// 跨页重名允许（load、realtime 等靠 PAGES[k] 显式索引调用），此处只禁页面层对共享层的覆盖。
func TestPageLayerDoesNotShadowShell(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未找到 node，跳过前端成员重名检查")
	}
	harness := filepath.Join(t.TempDir(), "shadow.cjs")
	if err := os.WriteFile(harness, []byte(shadowHarnessJS), 0o600); err != nil {
		t.Fatalf("写入 harness 失败: %v", err)
	}
	args := []string{harness, filepath.Join("assets", "app.js")}
	for _, p := range pages {
		args = append(args, filepath.Join("assets", "pages", p.key+".js"))
	}
	if out, err := exec.Command(node, args...).CombinedOutput(); err != nil {
		t.Fatalf("页面层与共享层存在重名成员: %v\n%s", err, out)
	}
}

// TestFrontendInitialLoad 用 node 驱动真实前端逻辑，守住「首屏页会加载数据」这一语义。
// Go 侧只能对 app.js 做源码文本 grep，而首屏加载 / 保活 / 会话边界恰好是纯行为问题；
// 无 node 时跳过。
func TestFrontendInitialLoad(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未找到 node，跳过前端行为测试")
	}
	if _, err := New(testVersion); err != nil { // 保证脚本清单与 New() 一致
		t.Fatalf("New() 失败: %v", err)
	}
	harness := filepath.Join(t.TempDir(), "harness.cjs")
	if err := os.WriteFile(harness, []byte(harnessJS), 0o600); err != nil {
		t.Fatalf("写入 harness 失败: %v", err)
	}
	args := []string{harness, filepath.Join("assets", "app.js")}
	for _, p := range pages {
		args = append(args, filepath.Join("assets", "pages", p.key+".js"))
	}
	if out, err := exec.Command(node, args...).CombinedOutput(); err != nil {
		t.Fatalf("前端行为测试失败: %v\n%s", err, out)
	}
}

// TestViewContainersBalanced .view 容器须平级（各自为 <main> 直接子节点），不得互相嵌套。
// 回归：HTML 注释误用 `*/` 收尾会吞掉后续真实标签，使部分 .view 被并入前一页的 <template
// x-if> 内：文档内 view 数量不减，进入 DOM 的减少，且无报错。
func TestViewContainersBalanced(t *testing.T) {
	h, _ := newTestRouter(t)
	for _, p := range h.Pages() {
		doc := string(p.HTML)
		// 扫描全文：整份文档本就应标签平衡（登录页/主界面模板亦需自洽），单独定位内容区反而复杂
		depth, viewDepths, err := scanTagBalance(doc)
		if err != nil {
			t.Errorf("%s: %v", p.Key, err)
			continue
		}
		if depth != 0 {
			t.Errorf("%s: 文档结束时标签深度 = %d，期望 0（有未闭合/多余闭合的标签，常见原因是注释吞掉了标签）", p.Key, depth)
		}
		if len(viewDepths) != len(h.Keys()) {
			t.Errorf("%s: .view 容器数 = %d，期望 %d", p.Key, len(viewDepths), len(h.Keys()))
			continue
		}
		want := viewDepths[0]
		for i, d := range viewDepths {
			if d != want {
				t.Errorf("%s: 第 %d 个 .view 的嵌套深度 = %d，与首个（%d）不同 —— 容器被嵌套了", p.Key, i, d, want)
			}
		}
	}
}

// scanTagBalance 扫描 HTML，返回收尾深度与各 <div class="view"> 出现时的嵌套深度。
// 须正确跳过注释：注释内的闭合标签仍会被朴素计数计入，深度看似平衡而结构已不同。
func scanTagBalance(doc string) (int, []int, error) {
	depth := 0
	var viewDepths []int
	// 参与平衡的标签：本前端仅用这三类嵌套（自闭合/空元素不参与）
	tracked := map[string]bool{"div": true, "section": true, "template": true}

	for i := 0; i < len(doc); {
		// 跳过 HTML 注释
		if strings.HasPrefix(doc[i:], "<!--") {
			end := strings.Index(doc[i+4:], "-->")
			if end < 0 {
				return depth, viewDepths, fmt.Errorf("位置 %d 起的 HTML 注释没有收尾（-->）——注释会吞掉后续标签", i)
			}
			i += 4 + end + 3
			continue
		}
		if doc[i] != '<' {
			i++
			continue
		}
		// 找到标签名
		j := i + 1
		closing := false
		if j < len(doc) && doc[j] == '/' {
			closing = true
			j++
		}
		k := j
		for k < len(doc) && (doc[k] >= 'a' && doc[k] <= 'z' || doc[k] >= 'A' && doc[k] <= 'Z') {
			k++
		}
		name := strings.ToLower(doc[j:k])
		tagEnd := strings.IndexByte(doc[i:], '>')
		if tagEnd < 0 {
			i++
			continue
		}
		tag := doc[i : i+tagEnd+1]

		if tracked[name] {
			if closing {
				depth--
				if depth < 0 {
					return depth, viewDepths, fmt.Errorf("位置 %d 的 </%s> 多余（没有对应开标签）", i, name)
				}
			} else {
				if name == "div" && strings.Contains(tag, `class="view"`) {
					viewDepths = append(viewDepths, depth)
				}
				depth++
			}
		}
		i += tagEnd + 1
	}
	return depth, viewDepths, nil
}
