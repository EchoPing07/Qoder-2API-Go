package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"qoder2api/admin"
	"qoder2api/store"
	"qoder2api/web"
)

// The admin API routes (/admin/api/*) and the WebUI routes (web.Handler.Mount)
// register on the same ServeMux with partially overlapping prefixes: a pattern
// conflict panics at registration time, and a missing registration 404s at
// runtime. Both packages' own tests mount in isolation, so this guards the
// exact assembly main() performs.
func TestAdminAPIAndWebUIRoutesCoexist(t *testing.T) {
	s, err := store.New(t.TempDir() + "/data.json")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	adminInst := admin.New(s, func(ctx context.Context) []string { return nil }, nil, nil)
	webHandler, err := web.New("vtest")
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}

	// A pattern conflict between the two registration sets panics right here.
	mux := http.NewServeMux()
	adminInst.RegisterRoutes(mux)
	webHandler.Mount(mux)

	cases := []struct {
		method string
		path   string
		code   int
	}{
		{"GET", "/admin", 200},               // WebUI stats page (namespace root)
		{"GET", "/admin/keys", 200},          // WebUI keys page
		{"GET", "/admin/assets/app.js", 200}, // WebUI asset
		{"GET", "/admin/api/auth", 200},      // admin API, no auth required
		{"GET", "/admin/api/stats", 401},     // admin API, auth required: proves the API is mounted
		{"POST", "/admin/api/login", 400},    // admin API login with an empty body
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.code)
		}
	}
}
