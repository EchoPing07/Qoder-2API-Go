// Package web 内嵌前端（go:embed，多页 HTML + 内嵌 Alpine.js，无外部依赖、无构建步骤）。
//
// 分层：
//
//	shell.html         静态壳层（head + 登录页 + 侧边栏 + 内容占位 + 确认弹窗 + toast）
//	pages/<key>.html   各页 section（+ 本页专属弹窗）
//	assets/app.css     样式表
//	assets/alpine.js   Alpine.js 运行时
//	assets/app.js      共享层 + 按序内联的各页脚本（PAGE 注册）
//
// 每个 URL 仍是独立文档（深链、刷新、新标签页直接可用，<title> 与 data-page 各自正确），
// 但每份文档的内容区都含全部页面：各页 section 包在 <div class="view" x-show="view==='<key>'">
// 内常驻，侧边栏 <a href> 由 app.js 拦截为软导航（切 view + pushState 换 URL）。
// 因此切页不重载文档、不重新请求资源，已加载数据保活。
//
// 路由布局（全部挂在 /admin 命名空间下，与 /admin/api/* 管理端点并行）：
//
//	/admin          统计页（命名空间根，页面清单首项）
//	/admin/keys     密钥页
//	/admin/token    令牌页
//	/admin/models   模型页
//	/admin/settings 设置页
//	/admin/assets/* 静态资源
//
// 代价：每份 HTML 含全部 5 个 view；页面在启动时渲染一次并缓存，请求只做字节写出
// （走 http.ServeContent，Content-Length / HEAD / 304 由其处理）。
//
// href 仅保证禁用 JS 时可到达正确 URL、新标签页可用：页面本体（登录页、主界面）都在
// <template x-if> 内，.view 均带 x-cloak，禁用 JS 时不会渲染内容。
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed shell.html pages/*.html
var pageFS embed.FS

//go:embed assets/app.css assets/app.js assets/alpine.js assets/pages/*.js
var assetFS embed.FS

// Page 一个页面 key 对应一份预渲染产物。
type Page struct {
	Key   string // 页面 key（路由 /admin/<key>；stats 对应命名空间根 /admin）
	Title string // 中文页名（<title> 与侧边栏）
	HTML  []byte // 渲染后的完整 HTML（启动时生成一次）
	ETag  string // 内容哈希 Etag（页面数十 KB 级，硬导航靠它换 304）
}

// pageDef 页面定义。
type pageDef struct {
	key   string // 页面 key，同时是资源名 pages/<key>.html、assets/pages/<key>.js
	title string // 中文页名
	icon  string // 侧边栏图标名（对应 assets/app.js 的 ICONS 表）
}

// pages 页面清单：顺序即侧边栏顺序，首项对应命名空间根 "/admin"。
var pages = []pageDef{
	{key: "stats", title: "统计", icon: "dashboard"},
	{key: "keys", title: "密钥", icon: "keys"},
	{key: "token", title: "令牌", icon: "token"},
	{key: "models", title: "模型", icon: "box"},
	{key: "settings", title: "设置", icon: "settings"},
}

// Brand 与 RepoURL：品牌名与项目主页的单一来源。
//
// 经模板注入 shell.html 的 <title>、登录页品牌标题与顶栏/侧栏 logo，改名或换仓库只需改此处。
// 品牌元素指向仓库（新标签页打开），站内首页由侧边栏「统计」项承担。
// 版本号不在此处：它是构建期注入值（ldflags），经 New(version) 传入，见 VersionDisplay。
const (
	Brand   = "Qoder-2API"
	RepoURL = "https://github.com/EchoPing07/Qoder-2API-Go"
)

// adminPrefix 是全部前端路由的命名空间根；统计页（清单首项）的规范路径就是它本身。
const adminPrefix = "/admin"

// VersionDisplay 归一版本号用于展示：空值回退 dev，非空时统一补 v 前缀。
//
// 版本的来源格式不一致：本地 go build 默认为 "dev"，release 二进制的 ldflags 注入值
// 是 git tag（已带 v）。展示层只负责把它收敛成同一个形状，不改变调用方传入的原始值。
func VersionDisplay(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "dev"
	}
	if strings.HasPrefix(v, "v") || v == "dev" {
		return v
	}
	return "v" + v
}

// pageKeyRe 页面 key 的合法字符集。
//
// key 同时用作文件名、URL 片段与 Alpine 表达式内的字符串字面量（:class="{active:view==='<key>'}"、
// x-html="ic('<icon>')"），故须保守；转义无法覆盖表达式内的引号（HTML 实体在 JS 解析前已解码）。
// 由 New() 启动期校验，非法即失败，不产出坏 HTML。
var pageKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// validPageKey 校验页面 key 是否合法。
func validPageKey(k string) bool { return pageKeyRe.MatchString(k) }

// contentETag 内容哈希 Etag（截 8 字节即可标识内嵌资源）。启动时重算，配合 no-cache 使刷新立即取到新脚本。
func contentETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// Handler 前端路由处理器：预渲染页面 + 内嵌资源。
type Handler struct {
	pages  map[string]*Page  // key → 预渲染页面
	order  []string          // 稳定顺序（= 侧边栏顺序）
	assets map[string][]byte // 资源路径（/admin/assets/…）→ 内容
	etags  map[string]string // 资源路径 → 内容哈希 Etag
}

// New 解析模板并渲染全部页面；任一环节失败都返回错误（启动期暴露，不静默降级）。
//
// version 为构建期注入的版本号（main 传入），仅用于登录页展示；
// 经 VersionDisplay 归一，空值回退 dev。该页不发任何请求，版本必须在首帧就位。
func New(version string) (*Handler, error) {
	displayVersion := VersionDisplay(version)
	for _, p := range pages {
		if !validPageKey(p.key) {
			return nil, fmt.Errorf("页面 key %q 非法（须匹配 %s）", p.key, pageKeyRe)
		}
		if p.title == "" {
			return nil, fmt.Errorf("页面 %s 缺少页名", p.key)
		}
	}

	shell, err := pageFS.ReadFile("shell.html")
	if err != nil {
		return nil, fmt.Errorf("读取 shell.html: %w", err)
	}
	tpl, err := template.New("shell").Parse(string(shell))
	if err != nil {
		return nil, fmt.Errorf("解析 shell.html: %w", err)
	}

	h := &Handler{
		pages:  make(map[string]*Page, len(pages)),
		assets: make(map[string][]byte, 3),
		etags:  make(map[string]string, 3),
	}

	// 内容区：全部页面各包一层 .view 常驻同一文档，未激活的由 x-show 隐藏。
	// 容器层不可省：同层级相邻的 x-show 不互斥，而 <template x-if> 每次切换都重建 DOM。
	var views strings.Builder
	for _, p := range pages {
		body, err := pageFS.ReadFile("pages/" + p.key + ".html")
		if err != nil {
			return nil, fmt.Errorf("读取 pages/%s.html: %w", p.key, err)
		}
		fmt.Fprintf(&views, "\n<div class=\"view\" x-cloak x-show=\"view==='%s'\">\n%s\n</div>\n", p.key, body)
	}

	// 逐页渲染。导航输出真实 <a href>（无 JS、新标签页可用），active 态由 Alpine 绑定。
	for _, p := range pages {
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, struct {
			Title   string
			Key     string
			Nav     template.HTML
			Content template.HTML
			Brand   string
			Repo    string
			Version string
		}{
			Title:   p.title,
			Key:     p.key,
			Nav:     renderNav(),
			Content: template.HTML(views.String()),
			Brand:   Brand,
			Repo:    RepoURL,
			Version: displayVersion,
		}); err != nil {
			return nil, fmt.Errorf("渲染 pages/%s.html: %w", p.key, err)
		}
		h.pages[p.key] = &Page{Key: p.key, Title: p.title, HTML: buf.Bytes(), ETag: contentETag(buf.Bytes())}
		h.order = append(h.order, p.key)
	}

	// 共享脚本 + 各页脚本：按 pages 清单顺序拼接。
	// 不能依赖 go:embed 的字典序：assets/pages/keys.js 会排在 assets/app.js 之前，
	// 在 PAGE 注册表定义前调用 PAGE()。
	var js bytes.Buffer
	appJS, err := assetFS.ReadFile("assets/app.js")
	if err != nil {
		return nil, fmt.Errorf("读取 assets/app.js: %w", err)
	}
	js.Write(appJS)
	for _, p := range pages {
		part, err := assetFS.ReadFile("assets/pages/" + p.key + ".js")
		if err != nil {
			return nil, fmt.Errorf("读取 assets/pages/%s.js: %w", p.key, err)
		}
		js.WriteString("\n\n")
		js.Write(part)
	}

	// 静态资源：内容哈希 Etag（启动时重算，配合 no-cache 使刷新立即取到新脚本）
	for _, a := range []struct{ path, file string }{
		{"/admin/assets/app.css", "assets/app.css"},
		{"/admin/assets/app.js", ""}, // 上面拼接好的 js
		{"/admin/assets/alpine.js", "assets/alpine.js"},
	} {
		body := js.Bytes()
		if a.file != "" {
			if body, err = assetFS.ReadFile(a.file); err != nil {
				return nil, fmt.Errorf("读取 %s: %w", a.file, err)
			}
		}
		h.assets[a.path] = body
		h.etags[a.path] = contentETag(body)
	}
	return h, nil
}

// Mount 把前端路由挂到标准库 mux 上：页面（含规范跳转）+ 静态资源。
//
// 路由表集中在此而非 main.go：测试与 main 复用同一份定义，避免复刻注册方式带来的漂移。
//
// 每个页面只有一个规范 URL，其余一律 308：
//
//	/admin/stats[/] → /admin      首页 key 为 stats，规范路径是命名空间根
//	/admin/keys/    → /admin/keys 其余页面的尾斜杠去掉
//
// 用 308 而非 301：同为永久重定向，但保留请求方法语义（RFC 7538），不会被中间层改写成 GET。
//
// 页面处理器内自查 method（GET/HEAD）：ServeMux 的方法前缀模式（"GET /admin/keys"）
// 也能达成同样效果，但 405 会是纯文本 body；这里保持与 /admin/api/* 一致的 JSON 错误体。
// /admin/api/* 由 admin 包另行注册，与这里互不重叠（更长前缀精确匹配优先）。
func (h *Handler) Mount(mux *http.ServeMux) {
	home := h.order[0]
	mux.HandleFunc(adminPrefix, pageMethodGuard(h.PageHandler(home)))
	// 首页别名与尾斜杠：永久跳回规范路径（避免手输该地址落到根 "/" 处理器的 404）
	mux.HandleFunc(adminPrefix+"/{$}", redirectPerm(adminPrefix))
	mux.HandleFunc(adminPrefix+"/"+home, redirectPerm(adminPrefix))
	mux.HandleFunc(adminPrefix+"/"+home+"/{$}", redirectPerm(adminPrefix))
	for _, key := range h.order[1:] {
		target := adminPrefix + "/" + key
		mux.HandleFunc(target, pageMethodGuard(h.PageHandler(key)))
		// 尾斜杠只作别名：308 到无尾斜杠版本，不做第二份正本
		mux.HandleFunc(target+"/{$}", redirectPerm(target))
	}
	mux.HandleFunc(adminPrefix+"/assets/", http.HandlerFunc(h.AssetHandler))
}

// pageMethodGuard 只放行 GET/HEAD，其余返回与 /admin/api/* 同形的 JSON 405。
//
// Allow 同时含 GET 与 HEAD：HEAD 复用 GET 的处理链（http.ServeContent 丢弃 body），
// 只写 "GET" 会误导客户端认为 HEAD 不受支持。
func pageMethodGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSONError(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		next(w, r)
	}
}

// redirectPerm 目标路径的规范跳转（308 永久重定向，见 Mount 注释）。
func redirectPerm(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
}

// renderNav 渲染侧边栏导航。
//
// href 是页面 key 的唯一来源（JS 由其推导、Go 由路由参数得），不另设映射表；active 态由
// Alpine 绑定：软导航不重渲染导航，服务端写死的高亮不会随切页变化。
//
// 输出经 template.HTMLEscapeString：key/icon/title 目前均受 pageKeyRe 与常量约束，
// 但拼接不该依赖上游约束（%q 是 Go 字面量转义，非 HTML 转义）。
func renderNav() template.HTML {
	var b strings.Builder
	for i, p := range pages {
		href := adminPrefix + "/" + p.key
		if i == 0 {
			href = adminPrefix // 统计页走命名空间根
		}
		fmt.Fprintf(&b, "\n        <a class=\"nav-link\" :class=\"{active:view==='%s'}\" href=\"%s\" @click=\"navClick($event)\">\n",
			template.HTMLEscapeString(p.key), template.HTMLEscapeString(href))
		fmt.Fprintf(&b, "          <span class=\"ic\" x-html=\"ic('%s')\"></span>\n", template.HTMLEscapeString(p.icon))
		fmt.Fprintf(&b, "          <span>%s</span>\n", template.HTMLEscapeString(p.title))
		b.WriteString("        </a>")
	}
	b.WriteString("\n      ")
	return template.HTML(b.String())
}

// Pages 返回全部页面（按清单顺序）。
func (h *Handler) Pages() []*Page {
	out := make([]*Page, 0, len(h.order))
	for _, k := range h.order {
		out = append(out, h.pages[k])
	}
	return out
}

// Keys 返回全部页面 key（按清单顺序）。
func (h *Handler) Keys() []string { return append([]string(nil), h.order...) }

// PageHandler 返回某页的 http.HandlerFunc；page 为空视作首页，未注册则 404。
//
// 与静态资源同一套缓存语义（no-cache + 内容哈希 Etag），复用 http.ServeContent：
// Content-Length / HEAD / If-None-Match → 304 由其处理，页面不整份重传。
func (h *Handler) PageHandler(page string) http.HandlerFunc {
	key := page
	if key == "" {
		key = h.order[0]
	}
	p, ok := h.pages[key]
	return func(w http.ResponseWriter, r *http.Request) {
		if !ok {
			writeJSONError(w, http.StatusNotFound, "页面不存在")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Etag", p.ETag)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(p.HTML))
	}
}

// AssetHandler 服务内嵌静态资源（/admin/assets/*）。
//
// 与 http.FileServerFS 的差别：不列目录（FileServer 会把 /admin/assets/ 渲染成文件清单）、
// 仅接受 GET/HEAD、显式声明 Content-Type、按内容哈希设 Etag 并支持 If-None-Match → 304。
// 页面与脚本用 no-cache（刷新即校验）；Alpine.js 体积大且几乎不变，沿用 1 天缓存。
func (h *Handler) AssetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSONError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	path := r.URL.Path
	body, ok := h.assets[path]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if path == "/admin/assets/alpine.js" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	}
	w.Header().Set("Etag", h.etags[path])
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// Assets 返回全部资源路径（字典序），供文档与调试使用。
func (h *Handler) Assets() []string {
	out := make([]string, 0, len(h.assets))
	for k := range h.assets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// writeJSONError 输出与 admin 包 API 端点同形的 JSON 错误体（{"error": …}）。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
