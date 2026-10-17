// HTTP surface of the bridge: the /v1/chat/completions and /v1/models
// handlers plus request validation and error mapping. Extracted from
// bridge.go; behaviour is unchanged.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qoder2api/auth"
	"qoder2api/logs"
	"qoder2api/models"
	"qoder2api/stats"
	"qoder2api/transform"
)

// ResolvedBridge is what a BridgeResolver returns: the bridge for the chosen
// account plus the authenticated key's metadata, which labels the request log.
// Only the key's ID and note are carried — never the raw key material.
type ResolvedBridge struct {
	Bridge  *OpenAiBridge
	KeyID   string // authenticated key's ID ("" when unknown)
	KeyNote string // authenticated key's note
	// AccountID identifies the Qoder account serving this request. Empty when
	// the caller does not track accounts (single-PAT deployments, tests). It
	// gives the failover callback the identity of the account that failed so
	// the router can exclude it from the retry.
	AccountID string
	// OnSuccess, when set, runs after the request completes without error. The
	// router uses it to bind the sticky session to the account only after a
	// real success (a failed account must never be pinned).
	OnSuccess func()
}

// ResolveRequest carries the routing inputs a resolver needs: the API key, the
// bare model name (region prefix already stripped), the parsed region filter,
// a conversation seed for sticky routing, and the accounts already tried by
// this request (excluded from re-selection during failover).
type ResolveRequest struct {
	APIKey     string
	Model      string
	Region     models.RegionFilter
	StickySeed string
	Exclude    map[string]bool
}

// FailoverFunc reports an upstream auth failure on the given account and asks
// the router for a replacement account (excluding every account already tried).
// It returns the replacement bridge (nil = give up, surface the original error).
// Implemented by the caller (main package); nil keeps the legacy behaviour.
type FailoverFunc func(failedAccountID string, req ResolveRequest, excluded map[string]bool) *ResolvedBridge

// UsageHook receives every usage frame together with the account that served
// the request (empty when unknown), enabling passive credit deduction for
// automatic switching. May be nil; it never affects the response.
type UsageHook func(accountID string, u *transform.Usage)

// BridgeResolver resolves an API key + routing request to the chosen bridge
// plus its key metadata, or nil if the key is invalid or no account is
// available. Implemented by the caller (main package).
type BridgeResolver func(req ResolveRequest) *ResolvedBridge

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

// maxFailoverAttempts is a defensive ceiling on the failover loop. The
// resolver's own MaxRetryAccounts normally ends it first; this only guards
// against a misbehaving callback.
const maxFailoverAttempts = 16

// MakeChatHandler creates the /v1/chat/completions handler. The resolver
// validates the API key, parses the optional region prefix from the model name,
// and returns the bridge for the routed account. rec records request statistics
// and lr audit-log entries (metadata only); both may be nil.
// failover may be nil: an upstream auth failure then surfaces as a plain
// 502 upstream_error. With it, an AuthError seen before any byte reached the
// client is reported to the callback, which routes to another account and
// returns a replacement bridge; the request is retried on it — invisible to
// the client.
func MakeChatHandler(resolver BridgeResolver, rec *stats.Recorder, lr *logs.Recorder, failover FailoverFunc, usageHook UsageHook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		token := extractBearerToken(r)
		if token == "" {
			writeError(w, 401, "invalid_request_error", "Missing Authorization: Bearer <key>")
			return
		}

		// Cap request body to bound memory use against oversized payloads.
		r.Body = http.MaxBytesReader(w, r.Body, chatMaxBodyBytes)

		var reqBody map[string]interface{}
		// UseNumber keeps client numbers as json.Number so their exact token
		// (1e18, 9007199254740993, long fractions) survives re-marshaling into
		// the gateway request verbatim instead of being rounded through float64.
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		if err := dec.Decode(&reqBody); err != nil {
			msg := truncateRunes("Invalid JSON body: "+err.Error(), 500)
			if lr != nil {
				lr.Record(&logs.LogEntry{StatusCode: 400, ErrorMsg: msg})
			}
			writeError(w, 400, "invalid_request_error", "Invalid JSON body: "+err.Error())
			return
		}

		// Optional region prefix on the model name (cn/ gl/ global/ auto/).
		// The bare model is what reaches the gateway; the prefix only steers
		// account selection.
		rawModel, _ := reqBody["model"].(string)
		regionFilter, bareModel := models.ParseRegionPrefix(rawModel)
		if bareModel != rawModel {
			reqBody["model"] = bareModel
		}

		routeReq := ResolveRequest{
			APIKey:     token,
			Model:      bareModel,
			Region:     regionFilter,
			StickySeed: firstUserContent(reqBody),
			Exclude:    map[string]bool{},
		}
		var rb *ResolvedBridge
		if resolver != nil {
			rb = resolver(routeReq)
		}
		if rb == nil || rb.Bridge == nil {
			writeError(w, 401, "invalid_request_error", "Invalid API key or no PAT configured")
			return
		}
		b := rb.Bridge

		// One log entry per authenticated request; auth failures are rejected
		// above and never logged. Fields are filled in below and the entry is
		// recorded by the deferred flush, the single exit point.
		entry := &logs.LogEntry{KeyID: rb.KeyID, KeyNote: rb.KeyNote}
		defer func() {
			entry.DurationMs = time.Since(start).Milliseconds()
			if lr != nil {
				lr.Record(entry)
			}
		}()

		// Only authenticated requests with a valid body are counted. The stats
		// label is the bare model so a prefixed request aggregates with the
		// unprefixed one.
		model := statsModelLabel(reqBody, b, r.Context())
		entry.Model = model
		entry.Stream, _ = reqBody["stream"].(bool)
		record := func(ok bool) {
			if rec != nil {
				rec.Record(model, ok)
			}
		}
		var usageSink UsageSink
		if rec != nil || lr != nil || usageHook != nil {
			usageSink = func(u *transform.Usage) {
				if rec != nil {
					rec.RecordUsage(model, &stats.Usage{
						PromptTokens:     u.PromptTokens,
						CompletionTokens: u.CompletionTokens,
						CachedTokens:     u.CachedPromptTokens(),
						Credits:          u.Credits,
						NonBillable:      !u.Billable,
					})
				}
				// Mirror the usage into the log entry as well.
				if u != nil {
					entry.PromptTokens = u.PromptTokens
					entry.CompletionTokens = u.CompletionTokens
					entry.TotalTokens = u.TotalTokens
					entry.Credits = u.Credits
					// Passive deduction for automatic switching; must not be
					// able to break the response path.
					if usageHook != nil {
						usageHook(rb.AccountID, u)
					}
				}
			}
		}

		// A failed account is excluded and the request retried on another one
		// when the upstream rejects the session credentials before any byte
		// reached the client (response still uncommitted). A StreamReportedError
		// (content already delivered) or a client disconnect (context.Canceled)
		// rules the response committed and skips the retry.
		for attempt := 0; ; attempt++ {
			err := b.HandleChat(r.Context(), w, reqBody, usageSink)
			if err == nil {
				if rb.OnSuccess != nil {
					rb.OnSuccess()
				}
				break
			}
			var authErr *auth.AuthError
			var repErr *StreamReportedError
			if failover != nil && attempt < maxFailoverAttempts && errors.As(err, &authErr) &&
				!errors.As(err, &repErr) && !errors.Is(err, context.Canceled) {
				// Exclude the account that just failed, then ask for another.
				routeReq.Exclude[rb.AccountID] = true
				if next := failover(rb.AccountID, routeReq, routeReq.Exclude); next != nil && next.Bridge != nil {
					log.Printf("[bridge] upstream auth failure on account %s; retrying on another account", rb.AccountID)
					rb, b = next, next.Bridge
					entry.KeyID, entry.KeyNote = next.KeyID, next.KeyNote
					continue
				}
			}
			handleChatError(w, entry, record, err, clientGone(err))
			return
		}
		entry.StatusCode = 200
		record(true)
	}
}

// firstUserContent returns the first user message's text, used as the sticky
// routing seed so a conversation keeps hitting the same account. Empty when no
// textual user message exists (routing then keys on the API key alone).
func firstUserContent(reqBody map[string]interface{}) string {
	msgs, _ := reqBody["messages"].([]interface{})
	for _, m := range msgs {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role != "user" {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			return truncateRunes(c, 256)
		case []interface{}:
			for _, part := range c {
				pm, ok := part.(map[string]interface{})
				if !ok {
					continue
				}
				if t, _ := pm["type"].(string); t == "text" {
					if txt, _ := pm["text"].(string); txt != "" {
						return truncateRunes(txt, 256)
					}
				}
			}
		}
	}
	return ""
}

// clientGone reports whether err is a client-initiated disconnect (or server
// shutdown): not a request failure, must not skew the success rate.
func clientGone(err error) bool {
	return errors.Is(err, context.Canceled)
}

// handleChatError maps a failed HandleChat call onto the client response and
// the request-log entry. Shared by the failover loop and the original path so
// both speak one error mapping.
func handleChatError(w http.ResponseWriter, entry *logs.LogEntry, record func(bool), err error, clientGone bool) {
	var repErr *StreamReportedError
	if errors.As(err, &repErr) {
		// Content was already delivered: the response is committed as
		// 200 with an in-band error chunk.
		entry.StatusCode, entry.ErrorMsg = 200, truncateRunes(repErr.Error(), 500)
		if !clientGone {
			record(false)
		}
		return
	}
	var paramErr *RequestParamError
	if errors.As(err, &paramErr) {
		entry.StatusCode, entry.ErrorMsg = 400, truncateRunes(paramErr.Error(), 500)
		writeError(w, 400, "invalid_request_error", paramErr.Error())
		record(false)
		return
	}
	var upErr *auth.UpstreamError
	if errors.As(err, &upErr) {
		// A gateway rejection of the request itself (bad parameter values,
		// unknown tiers, oversized caps...) passes through with its status
		// and body instead of being masked as a bridge fault.
		entry.StatusCode = clientStatusForUpstream(upErr.StatusCode)
		entry.ErrorMsg = truncateRunes("upstream: "+upErr.Body, 500)
		writeUpstreamRejection(w, upErr.StatusCode, upErr.Body, upErr.RetryAfter)
		record(false)
		return
	}
	var isErr *auth.InStreamError
	if errors.As(err, &isErr) {
		// The gateway accepted the request (HTTP 200) but failed inside the
		// SSE stream before any content was delivered: map by the status
		// embedded in the failure frame, same policy as UpstreamError. A
		// frame without a usable status is still a provider fault → 502.
		entry.StatusCode = clientStatusForUpstream(isErr.Status)
		entry.ErrorMsg = truncateRunes("upstream: "+isErr.Detail, 500)
		writeUpstreamRejection(w, isErr.Status, isErr.Detail, 0)
		record(false)
		return
	}
	var authErr *auth.AuthError
	if errors.As(err, &authErr) {
		// The gateway rejected OUR session/PAT credentials, not the
		// client's API key (that was validated by the resolver long before
		// this point). Passing the upstream 401 through would make every
		// client believe its own key is invalid, so this is reported as a
		// bad-gateway fault with the (redacted) upstream detail.
		entry.StatusCode, entry.ErrorMsg = 502, truncateRunes("upstream auth: "+authErr.Error(), 500)
		writeError(w, 502, "upstream_error", "upstream auth: "+authErr.Error())
		record(false)
		return
	}
	var valErr *models.UnsupportedModelError
	if errors.As(err, &valErr) {
		entry.StatusCode, entry.ErrorMsg = 400, truncateRunes(valErr.Error(), 500)
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
		entry.StatusCode, entry.ErrorMsg = 429, truncateRunes("upstream busy: "+busyErr.Message, 500)
		writeError(w, 429, "rate_limit_error", "upstream busy: "+busyErr.Message)
		record(false)
		return
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "Unsupported model") || strings.Contains(errMsg, "not supported") {
		entry.StatusCode, entry.ErrorMsg = 400, truncateRunes(errMsg, 500)
		writeError(w, 400, "invalid_request_error", errMsg)
		record(false)
		return
	}
	if clientGone {
		// Client aborted before anything was delivered: 499 (nginx's
		// "client closed request") rather than a misleading 500.
		entry.StatusCode, entry.ErrorMsg = 499, "client canceled"
	} else {
		entry.StatusCode, entry.ErrorMsg = 500, truncateRunes(errMsg, 500)
	}
	writeError(w, 500, "qoder_error", errMsg)
	if !clientGone {
		record(false)
	}
}

// clientStatusForUpstream maps an upstream status onto the client status
// writeUpstreamRejection sends, so the log also records what the client saw.
func clientStatusForUpstream(status int) int {
	switch {
	case status == 429:
		return 429
	case status >= 500 || status <= 0:
		return 502
	default:
		return 400
	}
}

// writeUpstreamRejection maps a gateway rejection onto the client response.
// status is the upstream status to mirror: 429 → 429 rate_limit_error (with
// Retry-After when the gateway sent one), ≥500 (or unknown, ≤0) → 502
// upstream_error, other 4xx → 400 invalid_request_error with the upstream
// body carried verbatim. Shared by the pre-flight (UpstreamError) and
// in-stream (InStreamError) paths so both speak the same convention.
func writeUpstreamRejection(w http.ResponseWriter, status int, body string, retryAfter time.Duration) {
	switch clientStatusForUpstream(status) {
	case 429:
		if retryAfter > 0 {
			retrySecs := int((retryAfter + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(retrySecs))
		}
		writeError(w, 429, "rate_limit_error", "upstream: "+body)
	case 502:
		writeError(w, 502, "upstream_error", "upstream: "+body)
	default:
		writeError(w, 400, "invalid_request_error", "upstream: "+body)
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
			if rb := resolver(ResolveRequest{APIKey: token, Region: models.RegionDefault}); rb != nil {
				b = rb.Bridge
			}
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
