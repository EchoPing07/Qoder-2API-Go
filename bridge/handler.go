// HTTP surface of the bridge: the /v1/chat/completions and /v1/models
// handlers plus request validation and error mapping. Extracted from
// bridge.go; behaviour is unchanged.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qoder2api/auth"
	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/transform"
)

// BridgeResolver resolves an API key to the current single OpenAiBridge.
// Returns nil if the key is invalid or no PAT is configured.
// The concrete implementation is provided by the caller (main package).
type BridgeResolver func(apiKey string) *OpenAiBridge

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimSpace(auth[7:])
		if token != "" {
			return token
		}
	}
	return ""
}

func writeError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	resp := map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
		},
	}
	b, _ := marshalNoEscape(resp)
	w.Write(b)
}

// MakeChatHandler creates the /v1/chat/completions handler.
// The resolver validates the API key and returns the single bridge instance.
// rec records per-request statistics; it may be nil.
func MakeChatHandler(resolver BridgeResolver, rec *stats.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeError(w, 401, "invalid_request_error", "Missing Authorization: Bearer <key>")
			return
		}
		var b *OpenAiBridge
		if resolver != nil {
			b = resolver(token)
		}
		if b == nil {
			writeError(w, 401, "invalid_request_error", "Invalid API key or no PAT configured")
			return
		}

		// Cap request body to bound memory use against oversized payloads.
		r.Body = http.MaxBytesReader(w, r.Body, chatMaxBodyBytes)

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			writeError(w, 400, "invalid_request_error", "Invalid JSON body: "+err.Error())
			return
		}

		// Only authenticated requests with a valid body are counted.
		model := statsModelLabel(reqBody, b, r.Context())
		record := func(ok bool) {
			if rec != nil {
				rec.Record(model, ok)
			}
		}
		var usageSink UsageSink
		if rec != nil {
			usageSink = func(u *transform.Usage) {
				rec.RecordUsage(&stats.Usage{
					PromptTokens:     u.PromptTokens,
					CompletionTokens: u.CompletionTokens,
					CachedTokens:     u.CachedPromptTokens(),
					Credits:          u.Credits,
					NonBillable:      !u.Billable,
				})
			}
		}

		err := b.HandleChat(r.Context(), w, reqBody, usageSink)
		if err != nil {
			// A client-initiated disconnect (or server shutdown) is not a
			// request failure and must not skew the success rate.
			clientGone := errors.Is(err, context.Canceled)
			var repErr *StreamReportedError
			if errors.As(err, &repErr) {
				if !clientGone {
					record(false)
				}
				return
			}
			var paramErr *RequestParamError
			if errors.As(err, &paramErr) {
				writeError(w, 400, "invalid_request_error", paramErr.Error())
				record(false)
				return
			}
			var valErr *models.UnsupportedModelError
			if errors.As(err, &valErr) {
				writeError(w, 400, "invalid_request_error", valErr.Error())
				record(false)
				return
			}
			var busyErr *auth.BusyError
			if errors.As(err, &busyErr) {
				// Gateway admission-control rejection, not a server fault:
				// surface it as 429 with Retry-After so clients back off.
				if busyErr.RetryAfter > 0 {
					retrySecs := int((busyErr.RetryAfter + time.Second - 1) / time.Second)
					w.Header().Set("Retry-After", strconv.Itoa(retrySecs))
				}
				writeError(w, 429, "rate_limit_error", "upstream busy: "+busyErr.Message)
				record(false)
				return
			}
			errMsg := err.Error()
			if strings.Contains(errMsg, "Unsupported model") || strings.Contains(errMsg, "not supported") {
				writeError(w, 400, "invalid_request_error", errMsg)
				record(false)
				return
			}
			writeError(w, 500, "qoder_error", errMsg)
			if !clientGone {
				record(false)
			}
			return
		}
		record(true)
	}
}

// MakeModelsHandler creates the /v1/models handler.
// The resolver validates the API key and returns the single bridge instance.
// If the key is invalid or no PAT is configured, the built-in default catalog
// is returned as a fallback.
func MakeModelsHandler(resolver BridgeResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		var b *OpenAiBridge
		if resolver != nil && token != "" {
			b = resolver(token)
		}
		if b != nil {
			catalog := b.GetCatalog(r.Context())
			payload := models.ModelsPayload(catalog)
			w.Header().Set("Content-Type", "application/json")
			bs, _ := marshalNoEscape(payload)
			w.Write(bs)
			return
		}
		payload := models.ModelsPayload(nil)
		w.Header().Set("Content-Type", "application/json")
		bs, _ := marshalNoEscape(payload)
		w.Write(bs)
	}
}

// statsModelLabel returns the model label used for request statistics.
// The label is capped at 64 chars since the request body size is unbounded,
// and an empty/invalid model falls back to the catalog default or "(default)".
func statsModelLabel(reqBody map[string]interface{}, b *OpenAiBridge, ctx context.Context) string {
	model, _ := reqBody["model"].(string)
	model = truncateRunes(model, 64)
	if model == "" {
		if b != nil {
			if catalog := b.GetCatalog(ctx); catalog != nil {
				if name, _, err := models.ResolveModel("", catalog); err == nil {
					model = name
				}
			}
		}
		if model == "" {
			model = "(default)"
		}
	}
	return model
}
