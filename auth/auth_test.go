package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []string{
		"hello world",
		`{"key":"value","number":42}`,
		"中文测试",
		"",
		"a",
		"ab",
		"abc",
		"abcd",
	}
	for _, tc := range tests {
		encoded := Encode([]byte(tc))
		decoded, err := Decode(encoded)
		if err != nil {
			t.Errorf("Decode(%q) error: %v", tc, err)
			continue
		}
		if !bytes.Equal(decoded, []byte(tc)) {
			t.Errorf("round-trip failed: got %q, want %q", string(decoded), tc)
		}
	}
}

func TestEncodeProducesNonStandardBase64(t *testing.T) {
	// The custom alphabet should produce different output from standard base64
	encoded := Encode([]byte("test"))
	// Should not contain standard base64 characters in the same positions
	if encoded == "" {
		t.Error("encode produced empty string")
	}
	// Should contain custom alphabet characters (not standard +/)
	for _, ch := range encoded {
		if ch == '+' || ch == '/' {
			t.Errorf("encode produced standard base64 char %q in %q", ch, encoded)
		}
	}
}

func TestSignDeterministic(t *testing.T) {
	date := "Mon, 22 Jul 2026 05:30:00 GMT"
	sig1 := Sign(date)
	sig2 := Sign(date)
	if sig1 != sig2 {
		t.Errorf("Sign not deterministic: %s vs %s", sig1, sig2)
	}
}

func TestSignKnownValue(t *testing.T) {
	// Verify sign produces a 32-char hex string (MD5)
	sig := Sign("test-date")
	if len(sig) != 32 {
		t.Errorf("Sign produced %d chars, expected 32", len(sig))
	}
}

func TestNewSession(t *testing.T) {
	identity := AuthIdentity{
		Name:               "test",
		Aid:                "id1",
		UID:                "id1",
		UserType:           "personal_standard",
		SecurityOauthToken: "sot",
		RefreshToken:       "rt",
	}
	sess := NewSession(identity, "machine-id", "machine-token", "machine-type")
	if sess == nil {
		t.Fatal("NewSession returned nil")
	}
	if sess.CosyKey == "" {
		t.Error("CosyKey is empty")
	}
	if sess.Info == "" {
		t.Error("Info is empty")
	}
	if len(sess.TempKey) != 16 {
		t.Errorf("TempKey length = %d, want 16", len(sess.TempKey))
	}
}

func TestBuildPayloadB64(t *testing.T) {
	b64 := BuildPayloadB64("test-info")
	if b64 == "" {
		t.Error("BuildPayloadB64 returned empty string")
	}
	// Should be valid standard base64
	decoded, err := decodeStd(b64)
	if err != nil {
		t.Errorf("BuildPayloadB64 produced invalid base64: %v", err)
	}
	if !bytes.Contains(decoded, []byte("test-info")) {
		t.Error("decoded payload doesn't contain info")
	}
	if !bytes.Contains(decoded, []byte("0.1.43")) {
		t.Error("decoded payload doesn't contain cosyVersion")
	}
}

func decodeStd(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// --- In-stream gateway error detection (real wire formats) ---

// Regression (real gateway, qmodel_preview provider outage): the stream
// opens with HTTP 200 but the first data frame carries a 429 envelope
// (provider_error "All backends failed"), followed by event:error and a
// bare success:false frame, then EOF without [DONE]. The terminal check
// must surface the real cause instead of "connection truncated".
func TestDetectInStreamGatewayErrorEnvelope429(t *testing.T) {
	line := `data:{"headers":{"Content-Type":["application/json"]},"body":"{\"code\":\"provider_error\",\"message\":\"All backends failed\",\"request_id\":\"x\",\"type\":\"provider_error\",\"param\":{\"retries\":2}}\n","statusCodeValue":429,"statusCode":"TOO_MANY_REQUESTS"}`
	e := detectInStreamGatewayError(line)
	if e == nil {
		t.Fatal("429 envelope must be detected as in-stream error")
	}
	if e.status != 429 {
		t.Errorf("status = %d, want 429", e.status)
	}
	if e.detail != "provider_error: All backends failed" {
		t.Errorf("detail = %q, want provider_error message", e.detail)
	}
}

// Normal envelope frames (200 OK chat/usage/[DONE] bodies) must NOT be
// treated as errors.
func TestDetectInStreamGatewayErrorOKEnvelope(t *testing.T) {
	for _, line := range []string{
		`data:{"body":"[DONE]","statusCodeValue":200,"statusCode":"OK"}`,
		`data:{"body":"{\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}","statusCodeValue":200,"statusCode":"OK"}`,
		`data:{"firstTokenDuration":799,"serverDuration":73,"totalDuration":2281}`,
		`data:{"body":"null","statusCodeValue":200,"statusCode":"OK"}`,
	} {
		if e := detectInStreamGatewayError(line); e != nil {
			t.Errorf("normal frame wrongly flagged: %q -> %v", line, e)
		}
	}
}

// The bare success:false / msgCode:500 frame that follows event:error.
func TestDetectInStreamGatewayErrorBareFailure(t *testing.T) {
	line := `data:{"success":false,"traceId":"t","msgCode":500,"msgInfo":"Internal Server Error","message":"Internal Server Error"}`
	e := detectInStreamGatewayError(line)
	if e == nil {
		t.Fatal("bare success:false frame must be detected")
	}
	if e.detail != "Internal Server Error" {
		t.Errorf("detail = %q", e.detail)
	}
}

// isErrorEvent / isFinishEvent classification.
func TestErrorAndFinishEventClassification(t *testing.T) {
	if !isErrorEvent("event:error") || !isErrorEvent("event: error ") {
		t.Error("event:error must be classified as error event")
	}
	if isErrorEvent("event:finish") || isErrorEvent("data: error") {
		t.Error("event:finish / data lines must not be error events")
	}
	if !isFinishEvent("event:finish") || isFinishEvent("event:error") {
		t.Error("event:finish classification broken")
	}
}

// trimJSONMessage extracts code+message from an escaped JSON body.
func TestTrimJSONMessage(t *testing.T) {
	got := trimJSONMessage(`{"code":"provider_error","message":"All backends failed","param":{"retries":2}}`)
	if got != "provider_error: All backends failed" {
		t.Errorf("got %q", got)
	}
	if got := trimJSONMessage("  plain text  "); got != "plain text" {
		t.Errorf("plain text passthrough: %q", got)
	}
	if got := trimJSONMessage(""); got != "" {
		t.Errorf("empty: %q", got)
	}
}

// -- Stream timeouts --

func TestStreamTimeoutsSetAndGet(t *testing.T) {
	defer SetStreamTimeouts(DefaultChatHeaderTimeout, DefaultChatIdleTimeout)
	got := CurrentStreamTimeouts()
	if got.Header != DefaultChatHeaderTimeout || got.Idle != DefaultChatIdleTimeout {
		t.Fatalf("expected defaults, got %+v", got)
	}
	// Zero values normalize to the defaults instead of disabling the timeout.
	SetStreamTimeouts(3*time.Second, 0)
	got = CurrentStreamTimeouts()
	if got.Header != 3*time.Second || got.Idle != DefaultChatIdleTimeout {
		t.Fatalf("expected {3s default}, got %+v", got)
	}
}

// testSession builds a minimal session for OpenStreamLines against a test
// server (the payload is never validated by the fake upstream).
func testSession() *SessionContext {
	return NewSession(AuthIdentity{UID: "u", Name: "n"}, "mid", "mtok", "mtype")
}

// A server that never starts responding must be cut off by the header
// timeout instead of hanging until the client disconnects.
func TestOpenStreamLinesHeaderTimeout(t *testing.T) {
	defer SetStreamTimeouts(DefaultChatHeaderTimeout, DefaultChatIdleTimeout)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	SetStreamTimeouts(50*time.Millisecond, time.Minute)
	start := time.Now()
	err := OpenStreamLines(context.Background(), testSession(), srv.URL, []byte(`{}`), nil, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected header timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("header timeout fired too late: %s", elapsed)
	}
}

// A stream that goes silent mid-response must be cut off by the idle
// timeout; the error must identify the idle timeout, not a truncation.
func TestOpenStreamLinesIdleTimeout(t *testing.T) {
	defer SetStreamTimeouts(DefaultChatHeaderTimeout, DefaultChatIdleTimeout)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		io.WriteString(w, "data: {\"body\":\"{}\"}\n\n")
		flusher.Flush()
		// Stall with the connection still open.
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	SetStreamTimeouts(time.Minute, 50*time.Millisecond)
	start := time.Now()
	err := OpenStreamLines(context.Background(), testSession(), srv.URL, []byte(`{}`), nil, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected idle timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "idle") {
		t.Errorf("error should mention idle timeout, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("idle timeout fired too late: %s", elapsed)
	}
}

// TestBusyErrorParsing locks the classification of gateway admission-control
// rejections (code 10605): they must surface as BusyError, never AuthError,
// regardless of whether they arrive as an HTTP 401/403 body or an in-stream
// control frame.
func TestBusyErrorParsing(t *testing.T) {
	// HTTP envelope: body prefixed with the upstream status ("403 {...").
	envBody := []byte(`403 {"code":"10605","message":"{\"isQueued\":false,\"modelKey\":\"qmodel_38max\",\"queueCount\":0,\"queueType\":\"slow\",\"retryAfterSeconds\":2,\"serviceAvailable\":true,\"waitTime\":0}"}`)
	busy := busyFromAuthEnvelope(401, envBody)
	if busy == nil {
		t.Fatal("expected BusyError from 10605 envelope, got nil")
	}
	if busy.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter = %v, want 2s", busy.RetryAfter)
	}

	// A genuine auth failure must still classify as AuthError.
	if busyFromAuthEnvelope(403, []byte(`{"code":"401","message":"unauthorized"}`)) != nil {
		t.Error("non-10605 body must not be a BusyError")
	}

	// In-stream frame with statusCodeValue 403 + code 10605.
	frame := `data: {"body":"{\"code\":\"10605\",\"message\":\"{\\\"retryAfterSeconds\\\":2}\"}","statusCode":"FORBIDDEN","statusCodeValue":403}`
	inStream := detectInStreamBusyError(frame)
	if inStream == nil {
		t.Fatal("expected BusyError from in-stream 10605 frame, got nil")
	}
	if inStream.RetryAfter != 2*time.Second {
		t.Errorf("in-stream RetryAfter = %v, want 2s", inStream.RetryAfter)
	}
	// ...and must NOT be misread as an auth error.
	if isAuth, _ := detectInStreamAuthError(frame); isAuth {
		t.Error("10605 frame must not be classified as an auth error")
	}
}

// Numeric "code" variants (10605 as a JSON number instead of a string) must
// still classify as busy, on both the HTTP-envelope and in-stream paths.
func TestBusyErrorNumericCode(t *testing.T) {
	env := []byte(`{"code":10605,"message":"{\"retryAfterSeconds\":3}"}`)
	busy := busyFromAuthEnvelope(403, env)
	if busy == nil {
		t.Fatal("expected BusyError for numeric code, got nil")
	}
	if busy.RetryAfter != 3*time.Second {
		t.Errorf("RetryAfter = %v, want 3s", busy.RetryAfter)
	}

	frame := `data: {"body":"{\"code\":10605,\"message\":\"{}\"}","statusCodeValue":401}`
	if detectInStreamBusyError(frame) == nil {
		t.Fatal("expected BusyError for numeric in-stream code, got nil")
	}
}

// A truncated 10605 body (larger than the read cap) must not silently fall
// back to AuthError classification.
func TestBusyErrorFromLongBody(t *testing.T) {
	longMsg := `{"isQueued":false,"modelKey":"` + strings.Repeat("m", 4096) + `","retryAfterSeconds":2}`
	body := []byte(`{"code":"10605","message":` + strconv.Quote(longMsg) + `}`)
	if busyFromAuthEnvelope(401, body) == nil {
		t.Fatal("expected BusyError for long 10605 body within read cap, got nil")
	}
}

// The upstream-provided message echoed into errors must be length-capped.
func TestBusyErrorMessageCapped(t *testing.T) {
	msg := strings.Repeat("x", 10000)
	busy := busyFromAuthEnvelope(403, []byte(`{"code":"10605","message":"`+msg+`"}`))
	if busy == nil {
		t.Fatal("expected BusyError")
	}
	if got := len([]rune(busy.Message)); got > maxBusyMessageRunes+10 {
		t.Errorf("message not capped: %d runes", got)
	}
}

// Content-delta frames without a statusCodeValue envelope must skip the busy
// pre-filter cheaply (no false positives either way).
func TestDetectInStreamBusyIgnoresPlainDeltas(t *testing.T) {
	line := `data: {"body":"{\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}"}`
	if detectInStreamBusyError(line) != nil {
		t.Error("plain content delta must not be classified as busy")
	}
}
