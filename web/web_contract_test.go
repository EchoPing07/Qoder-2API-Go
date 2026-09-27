package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"qoder2api/admin"
	"qoder2api/store"
)

// apiPathRe matches the /admin/api/<name> literals the frontend fetches.
// A '?' terminator (query strings like '/admin/api/keys?id=…') ends the match,
// so only the path itself is collected.
var apiPathRe = regexp.MustCompile(`/admin/api/[a-z]+`)

// TestFrontendAPIPathsMatchAdminRoutes is the contract between the embedded
// frontend and the JSON API: every /admin/api/* path the JS actually calls
// must resolve to a registered admin route. The two sides live in different
// packages and evolve separately (this very refactor moved the UI), so a
// rename on one side would otherwise fail silently as a runtime 404 toast.
func TestFrontendAPIPathsMatchAdminRoutes(t *testing.T) {
	files := []string{"assets/app.js"}
	for _, p := range pages {
		files = append(files, "assets/pages/"+p.key+".js")
	}
	paths := map[string]bool{}
	for _, f := range files {
		b, err := assetFS.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s: %v", f, err)
		}
		for _, m := range apiPathRe.FindAllString(string(b), -1) {
			paths[m] = true
		}
	}
	// The shared layer alone calls at least the auth/login/logout trio; an
	// empty set means the scanner itself broke, not that the frontend calls
	// nothing.
	if len(paths) < 3 {
		t.Fatalf("在 %d 个文件里只找到 %d 个 API 路径，扫描器可能坏了", len(files), len(paths))
	}

	st, err := store.New(t.TempDir() + string(os.PathSeparator) + "data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	a := admin.New(st, func(ctx context.Context) []string { return nil }, nil, nil)
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)

	for p := range paths {
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, p, nil))
		if pattern == "" {
			t.Errorf("前端调用了 %s，但它没有注册在 admin mux 上（运行时会得到 404）", p)
		}
	}
}
