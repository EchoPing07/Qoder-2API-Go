package main

import (
	"net/http"
	"strings"
)

// corsPolicy is parsed from QODER_CORS_ORIGINS: either "*" or a comma-separated
// list of full origins (scheme://host[:port]). An empty spec disables CORS
// entirely, which keeps the previous behaviour for existing deployments.
//
// Only the OpenAI-compatible API and /health are covered. The admin UI stays
// same-origin on purpose: it authenticates with a session cookie, so allowing
// cross-origin calls would widen the CSRF surface for no legitimate client.
type corsPolicy struct {
	allowAll bool
	origins  map[string]struct{}
}

func parseCORSPolicy(spec string) corsPolicy {
	p := corsPolicy{origins: map[string]struct{}{}}
	for _, part := range strings.Split(spec, ",") {
		origin := strings.TrimRight(strings.TrimSpace(part), "/")
		switch {
		case origin == "":
			continue
		case origin == "*":
			p.allowAll = true
		default:
			p.origins[strings.ToLower(origin)] = struct{}{}
		}
	}
	return p
}

func (p corsPolicy) enabled() bool {
	return p.allowAll || len(p.origins) > 0
}

func (p corsPolicy) allows(origin string) bool {
	if origin == "" {
		return false
	}
	if p.allowAll {
		return true
	}
	_, ok := p.origins[strings.ToLower(strings.TrimRight(origin, "/"))]
	return ok
}

// corsHandler adds CORS headers to the API surface and answers preflight
// requests before they reach the handlers (which only accept GET/POST and would
// otherwise reject OPTIONS). When the policy is empty the handler is untouched.
func corsHandler(spec string, next http.Handler) http.Handler {
	policy := parseCORSPolicy(spec)
	if !policy.enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCORSPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); policy.allows(origin) {
			if policy.allowAll {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", strings.TrimRight(origin, "/"))
			}
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			// Echo the requested headers so SDKs that send extra fields (e.g.
			// X-Api-Key, OpenAI-Organization) survive preflight. Browsers cannot
			// wildcard the Authorization header, so an explicit list is the safe
			// default when none was requested.
			allowed := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
			if allowed == "" {
				allowed = "Authorization, Content-Type"
			}
			h.Set("Access-Control-Allow-Headers", allowed)
			h.Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isCORSPath(path string) bool {
	return path == "/health" || strings.HasPrefix(path, "/v1/")
}
