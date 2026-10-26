package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recordingHandler(calls *int, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.WriteHeader(status)
	})
}

func staticCORSSpec(spec string) func() string {
	return func() string { return spec }
}

func TestParseCORSPolicy(t *testing.T) {
	cases := []struct {
		spec     string
		enabled  bool
		allowAll bool
		allows   map[string]bool
	}{
		{"", false, false, map[string]bool{"https://a.com": false}},
		{" , ,", false, false, map[string]bool{"https://a.com": false}},
		{"*", true, true, map[string]bool{"https://a.com": true, "http://b.test:8080": true}},
		{
			" https://A.com/ , https://b.test:8080 ",
			true, false,
			map[string]bool{
				"https://a.com":       true, // host case is not significant
				"https://b.test:8080": true,
				"https://c.com":       false,
				"https://a.com.evil":  false,
			},
		},
	}
	for _, c := range cases {
		p := parseCORSPolicy(c.spec)
		if p.enabled() != c.enabled {
			t.Errorf("spec %q: enabled = %v, want %v", c.spec, p.enabled(), c.enabled)
		}
		if p.allowAll != c.allowAll {
			t.Errorf("spec %q: allowAll = %v, want %v", c.spec, p.allowAll, c.allowAll)
		}
		for origin, want := range c.allows {
			if got := p.allows(origin); got != want {
				t.Errorf("spec %q: allows(%q) = %v, want %v", c.spec, origin, got, want)
			}
		}
	}
}

func TestCORSDisabledLeavesRequestsUntouched(t *testing.T) {
	calls := 0
	h := corsHandler(staticCORSSpec(""), recordingHandler(&calls, http.StatusOK))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Origin", "https://any.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("request not passed through: code=%d calls=%d", rec.Code, calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disabled policy must not emit CORS headers, got %q", got)
	}
}

func TestCORSAllowAllPreflight(t *testing.T) {
	calls := 0
	h := corsHandler(staticCORSSpec("*"), recordingHandler(&calls, http.StatusOK))

	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization, x-api-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if calls != 0 {
		t.Errorf("preflight must not reach the handler, calls = %d", calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "authorization, x-api-key" {
		t.Errorf("Allow-Headers = %q, want the requested list echoed", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Errorf("Allow-Methods = %q, want POST", got)
	}
}

func TestCORSAllowlistReflectsOrigin(t *testing.T) {
	calls := 0
	h := corsHandler(staticCORSSpec("https://app.example.com"), recordingHandler(&calls, http.StatusUnauthorized))

	allowed := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	allowed.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, allowed)

	// Error responses must carry CORS headers too, otherwise the browser hides them.
	if rec.Code != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("unexpected response: code=%d calls=%d", rec.Code, calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want the requesting origin", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Errorf("Vary = %q, want Origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization, Content-Type" {
		t.Errorf("default Allow-Headers = %q", got)
	}

	disallowed := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	disallowed.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, disallowed)

	if rec.Code != http.StatusUnauthorized || calls != 2 {
		t.Fatalf("disallowed request should still pass through: code=%d calls=%d", rec.Code, calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin must not receive Allow-Origin, got %q", got)
	}
}

func TestCORSIgnoresAdminRoutes(t *testing.T) {
	calls := 0
	h := corsHandler(staticCORSSpec("*"), recordingHandler(&calls, http.StatusOK))

	req := httptest.NewRequest(http.MethodGet, "/admin/api/auth", nil)
	req.Header.Set("Origin", "https://any.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if calls != 1 {
		t.Errorf("admin request not passed through, calls = %d", calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("admin routes must stay outside CORS, got Allow-Origin %q", got)
	}
}

func TestCORSHealthAllowed(t *testing.T) {
	calls := 0
	h := corsHandler(staticCORSSpec("*"), recordingHandler(&calls, http.StatusOK))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("health not passed through: code=%d calls=%d", rec.Code, calls)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want *", got)
	}
}

func TestCORSHotReload(t *testing.T) {
	spec := ""
	calls := 0
	h := corsHandler(func() string { return spec }, recordingHandler(&calls, http.StatusOK))

	allowOrigin := func(origin string) string {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Header().Get("Access-Control-Allow-Origin")
	}

	if got := allowOrigin("https://app.example"); got != "" {
		t.Fatalf("disabled CORS emitted Allow-Origin %q", got)
	}
	spec = "https://app.example"
	if got := allowOrigin("https://app.example"); got != "https://app.example" {
		t.Fatalf("enabled CORS not applied, Allow-Origin = %q", got)
	}
	spec = ""
	if got := allowOrigin("https://app.example"); got != "" {
		t.Fatalf("disabled CORS after reload emitted Allow-Origin %q", got)
	}
}
