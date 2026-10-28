package main

import (
	"net/http"
	"net/url"
	"strings"
)

// corsPolicy is parsed from a spec of either "*" or a comma-separated list of
// full origins (scheme://host[:port]). The spec is supplied by main from the
// env override or the admin panel's saved settings; an empty spec disables
// CORS entirely.
//
// Only the OpenAI-compatible API and /health are covered. The admin UI stays
// same-origin on purpose: it authenticates with a session cookie, so allowing
// cross-origin calls would widen the CSRF surface for no legitimate client.
type corsPolicy struct {
	allowAll bool
	origins  map[string]struct{}
}

// normalizeOrigin canonicalizes one origin for comparison: trimmed, no trailing
// slash, lowercase host, and the scheme's default port dropped. Browsers omit
// :80/:443 from Origin, so "https://a.com:443" would otherwise never match
// anything a browser actually sends.
//
// Applied to both sides of the comparison rather than to the stored value, so
// the matcher is correct no matter what is already in data.json or in the env
// var, and there is only one place to get the rule right.
func normalizeOrigin(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" || origin == "*" {
		return origin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		// Not a URL we can reason about. Keep the lowercased form so the result
		// still depends only on the input; ValidateCORSOrigins is what rejects
		// such a value before it can be saved.
		return strings.ToLower(origin)
	}
	host := strings.ToLower(u.Host)
	if u.Scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	if u.Scheme == "http" {
		host = strings.TrimSuffix(host, ":80")
	}
	return u.Scheme + "://" + host
}

func parseCORSPolicy(spec string) corsPolicy {
	p := corsPolicy{origins: map[string]struct{}{}}
	for _, part := range strings.Split(spec, ",") {
		origin := normalizeOrigin(part)
		switch {
		case origin == "":
			continue
		case origin == "*":
			p.allowAll = true
		default:
			p.origins[origin] = struct{}{}
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
	_, ok := p.origins[normalizeOrigin(origin)]
	return ok
}

// corsHandler adds CORS headers to the API surface and answers preflight
// requests before they reach the handlers (OPTIONS is not part of their
// contract: /health answers 405 and the chat handler answers 401). The spec is
// read per request so admin-panel changes take effect without a restart.
func corsHandler(spec func() string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCORSPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" && r.Method != http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		policy := parseCORSPolicy(spec())
		if !policy.enabled() {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		if policy.allows(origin) {
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
