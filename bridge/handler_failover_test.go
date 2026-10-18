package bridge

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"qoder2api/auth"
)

// failoverGateway is a fake upstream whose chat endpoint 401s the first N
// requests (invalid session token) before serving success, so the failover
// retry path can be observed end to end.
type failoverGateway struct {
	unauthorizedFirstN int
	// quotaFirstN 402s the first N requests with a quota-exhaustion body
	// (real-time quota failover classification).
	quotaFirstN int
	// badRequestFirstN 400s the first N requests with a non-quota body —
	// a parameter rejection must NOT trigger the failover path.
	badRequestFirstN int
	// authFrameAfterContent delivers an in-stream auth failure (top-level
	// statusCodeValue) after the content frame, so handleStream wraps the
	// AuthError in StreamReportedError (response already committed).
	authFrameAfterContent bool
	chatHits              int
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func newFailoverBridge(t *testing.T, gw *failoverGateway) *OpenAiBridge {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/algo/api/v3/user/jobToken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"tester","id":"uid1","userType":"personal_standard","refreshToken":"rt","securityOauthToken":"sot","expireTime":` + itoa64(time.Now().Add(6*time.Hour).UnixMilli()) + `}`))
	})
	mux.HandleFunc("/algo/api/v2/model/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"chat":[{"key":"qmodel_latest","display_name":"Fake-Model","enable":true,"is_vl":false}]}`))
	})
	mux.HandleFunc("/algo/api/v2/service/pro/sse/agent_chat_generation", func(w http.ResponseWriter, r *http.Request) {
		gw.chatHits++
		if gw.chatHits <= gw.unauthorizedFirstN {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`401 {"code":"40x","message":"session invalid"}`))
			return
		}
		if gw.chatHits <= gw.quotaFirstN {
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte(`{"message":"quota exceeded, waiting for reset"}`))
			return
		}
		if gw.chatHits <= gw.badRequestFirstN {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message":"max_tokens out of range"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		lines := []string{
			sseFrame(t, map[string]interface{}{"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{"content": "ok"}}}}),
		}
		if gw.authFrameAfterContent {
			lines = append(lines, `data: {"statusCodeValue":401,"message":"session invalid"}`)
		} else {
			lines = append(lines, "data: [DONE]")
		}
		for _, l := range lines {
			w.Write([]byte(l + "\n\n"))
			flusher.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	region := &auth.RegionConfig{Name: "test", AuthBase: srv.URL, ChatBase: srv.URL}
	return NewOpenAiBridge("pt-test", region)
}

func failoverChatReq(stream bool) string {
	body := `{"model":"Fake-Model","messages":[{"role":"user","content":"hi"}]`
	if stream {
		body += `,"stream":true`
	}
	return body + `}`
}

// Failover retry: the first bridge 401s before any byte is delivered, the
// callback returns a healthy bridge, and the client sees the successful
// completion — one request, no error surfaced.
func TestChatHandlerFailoverRetriesOnAuthError(t *testing.T) {
	gwBad := &failoverGateway{unauthorizedFirstN: 99}
	bad := newFailoverBridge(t, gwBad)
	gwGood := &failoverGateway{}
	good := newFailoverBridge(t, gwGood)

	calls := 0
	failover := func(failedAccountID string, quotaExhausted bool) *ResolvedBridge {
		calls++
		if quotaExhausted {
			t.Error("an auth failure must not be classified as quota exhaustion")
		}
		return &ResolvedBridge{Bridge: good, AccountID: "acc2"}
	}
	handler := MakeChatHandler(
		func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: bad, AccountID: "acc1"} },
		nil, nil, failover, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(failoverChatReq(true)))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover retry\n%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content":"ok"`) {
		t.Errorf("body should carry the retried success:\n%s", w.Body.String())
	}
	if calls != 1 {
		t.Errorf("failover calls = %d, want 1", calls)
	}
	if gwGood.chatHits != 1 {
		t.Errorf("good gateway chat hits = %d, want 1", gwGood.chatHits)
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Errorf("stream should complete with [DONE]:\n%s", w.Body.String())
	}
}

// When the failover callback gives up (no candidate), the legacy AuthError
// mapping (502 upstream_error) is preserved.
func TestChatHandlerFailoverGivesUpSurfaces502(t *testing.T) {
	gw := &failoverGateway{unauthorizedFirstN: 99}
	bad := newFailoverBridge(t, gw)

	failover := func(string, bool) *ResolvedBridge { return nil }
	handler := MakeChatHandler(
		func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: bad, AccountID: "acc1"} },
		nil, nil, failover, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(failoverChatReq(true)))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502\n%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "upstream auth") {
		t.Errorf("body should carry the upstream auth mapping:\n%s", w.Body.String())
	}
}

// Real-time quota failover: the first bridge gets an HTTP 402 quota-exhaustion
// rejection before any byte is delivered; the callback is invoked with
// quotaExhausted=true and, on a healthy replacement, the client sees the
// successful completion — one request, no error surfaced.
func TestChatHandlerFailoverRetriesOnQuotaExhausted(t *testing.T) {
	gwBad := &failoverGateway{quotaFirstN: 99}
	bad := newFailoverBridge(t, gwBad)
	gwGood := &failoverGateway{}
	good := newFailoverBridge(t, gwGood)

	calls := 0
	failover := func(failedAccountID string, quotaExhausted bool) *ResolvedBridge {
		calls++
		if !quotaExhausted {
			t.Error("a quota rejection must be classified as quota exhaustion")
		}
		return &ResolvedBridge{Bridge: good, AccountID: "acc2"}
	}
	handler := MakeChatHandler(
		func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: bad, AccountID: "acc1"} },
		nil, nil, failover, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(failoverChatReq(true)))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after quota failover retry\n%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content":"ok"`) {
		t.Errorf("body should carry the retried success:\n%s", w.Body.String())
	}
	if calls != 1 || gwGood.chatHits != 1 {
		t.Errorf("failover calls = %d, good hits = %d, want 1/1", calls, gwGood.chatHits)
	}
}

// A parameter rejection (HTTP 400, non-quota body) is a request fault, not an
// account fault: no failover, and the legacy 400 mapping is preserved.
func TestChatHandlerNoFailoverOnBadRequest(t *testing.T) {
	gw := &failoverGateway{badRequestFirstN: 99}
	bad := newFailoverBridge(t, gw)

	calls := 0
	failover := func(string, bool) *ResolvedBridge {
		calls++
		return nil
	}
	handler := MakeChatHandler(
		func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: bad, AccountID: "acc1"} },
		nil, nil, failover, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(failoverChatReq(true)))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\n%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Errorf("failover fired %d time(s) on a parameter rejection, want 0", calls)
	}
}

// A stream auth failure AFTER content was delivered arrives wrapped in
// StreamReportedError: the response is committed, so failover must not fire
// and the retry must not append a second copy of the stream.
func TestChatHandlerFailoverSkipsCommittedStream(t *testing.T) {
	gwBad := &failoverGateway{authFrameAfterContent: true}
	bad := newFailoverBridge(t, gwBad)
	good := newFailoverBridge(t, &failoverGateway{})

	calls := 0
	failover := func(string, bool) *ResolvedBridge {
		calls++
		return &ResolvedBridge{Bridge: good, AccountID: "acc2"}
	}
	handler := MakeChatHandler(
		func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: bad, AccountID: "acc1"} },
		nil, nil, failover, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(failoverChatReq(true)))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (committed SSE)\n%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if calls != 0 {
		t.Errorf("failover fired %d time(s) on a committed response, want 0", calls)
	}
	if strings.Count(body, `"content":"ok"`) != 1 {
		t.Errorf("committed stream must not be retried (content duplicated):\n%s", body)
	}
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("failed stream must not terminate with [DONE]:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"error"`) {
		t.Errorf("SSE error chunk must be present:\n%s", body)
	}
}

// A nil failover keeps the legacy mapping: AuthError → 502.
func TestChatHandlerNilFailoverPreservesLegacyMapping(t *testing.T) {
	gw := &failoverGateway{unauthorizedFirstN: 99}
	bad := newFailoverBridge(t, gw)
	handler := MakeChatHandler(
		func(string) *ResolvedBridge { return &ResolvedBridge{Bridge: bad, AccountID: "acc1"} },
		nil, nil, nil, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(failoverChatReq(false)))
	req.Header.Set("Authorization", "Bearer sk-test")
	handler(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502\n%s", w.Code, w.Body.String())
	}
}
