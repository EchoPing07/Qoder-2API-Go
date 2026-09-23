package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

func TestFetchQuotaUsageUsesSecurityOauthBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/quota/usage" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sot-test" {
			t.Errorf("Authorization = %q, want security OAuth bearer", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"userId":"u1","userType":"teams","totalUsagePercentage":0.98,"isQuotaExceeded":false,"expiresAt":1790265600000,"userQuota":{"total":3000,"used":2939,"remaining":61,"percentage":0.98,"unit":"credits"},"orgResourcePackage":{"used":0,"cap":4000,"remaining":0,"percentage":0,"available":false,"unit":"credits"}}`)
	}))
	defer srv.Close()

	usage, err := FetchQuotaUsage(context.Background(), "sot-test", &RegionConfig{OpenAPIBase: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if usage.UserQuota == nil || usage.UserQuota.Used != 2939 || usage.UserQuota.Remaining != 61 {
		t.Errorf("unexpected user quota: %+v", usage.UserQuota)
	}
	if usage.OrgResourcePackage == nil || usage.OrgResourcePackage.Cap != 4000 || usage.OrgResourcePackage.Available {
		t.Errorf("unexpected organization package: %+v", usage.OrgResourcePackage)
	}
	if usage.ExpiresAtMs != 1790265600000 || usage.TotalUsagePercentage != 0.98 {
		t.Errorf("unexpected cycle metadata: %+v", usage)
	}
}

func TestFetchQuotaUsageRequiresBearer(t *testing.T) {
	if _, err := FetchQuotaUsage(context.Background(), "", CN); err == nil {
		t.Fatal("expected an error for an empty security OAuth token")
	}
}

// §8: the once-a-minute quota refresh must not log the response headers or
// body (userId, quota details, trace ids); the summary stays status+duration.
func TestFetchQuotaUsageResponseLogRedactsBodyAndHeaders(t *testing.T) {
	const userID = "secret-user-id-42"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trace-Id", "trace-secret-99")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"userId":"`+userID+`","userType":"teams","userQuota":{"total":1,"used":0,"remaining":1,"unit":"credits"}}`)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)

	if _, err := FetchQuotaUsage(context.Background(), "sot-test", &RegionConfig{OpenAPIBase: srv.URL}); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if strings.Contains(out, userID) {
		t.Errorf("response body leaked into logs: %s", out)
	}
	if strings.Contains(out, "trace-secret-99") {
		t.Errorf("response headers leaked into logs: %s", out)
	}
	if !strings.Contains(out, "[auth] OpenAPI response:") || !strings.Contains(out, "status=200") {
		t.Errorf("expected a status+duration response summary, got: %s", out)
	}
}

// A non-200 quota response embeds its body in the returned error, which the
// account loop logs at WARN. The identity fields must not survive that path
// either: the error carries at most a short, single-line prefix.
func TestFetchQuotaUsageErrorTruncatesBody(t *testing.T) {
	const userID = "secret-user-id-42"
	// Longer than the cap, so truncation is observable, with the identity field
	// placed after the cut.
	padding := strings.Repeat("x", 400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"error":"upstream down","pad":"`+padding+`","userId":"`+userID+`"}`)
	}))
	defer srv.Close()

	_, err := FetchQuotaUsage(context.Background(), "sot-test", &RegionConfig{OpenAPIBase: srv.URL})
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("expected the HTTP status in the error, got %q", msg)
	}
	if strings.Contains(msg, userID) {
		t.Errorf("the error leaked the response identity: %q", msg)
	}
	if len(msg) > 400 {
		t.Errorf("expected a truncated body in the error, got %d chars: %q", len(msg), msg)
	}
}

// The upstream error body is flattened to a single line so it cannot forge log
// entries or split the WARN into several lines, and identity-valued fields are
// replaced before the length cap so a short but sensitive payload cannot slip
// through intact.
func TestTruncateErrorBodyRedactsAndFlattens(t *testing.T) {
	got := truncateErrorBody([]byte("line1\nline2\r\nline3"))
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("expected a single line, got %q", got)
	}
	if got != "line1 line2  line3" {
		t.Errorf("unexpected flattening: %q", got)
	}

	// A sensitive field that fits well inside the cap must still be redacted.
	got = truncateErrorBody([]byte(`{"userId":"u-42","traceId":"t-99","error":"down"}`))
	for _, secret := range []string{"u-42", "t-99"} {
		if strings.Contains(got, secret) {
			t.Errorf("identity value %q survived redaction: %q", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected the redaction marker, got %q", got)
	}
	if !strings.Contains(got, `"error":"down"`) {
		t.Errorf("redaction must preserve non-identity fields, got %q", got)
	}

	long := strings.Repeat("a", maxErrorBodyChars+50)
	got = truncateErrorBody([]byte(long))
	if len([]rune(got)) != maxErrorBodyChars+1 { // +1 for the ellipsis
		t.Errorf("expected a capped body of %d chars, got %d", maxErrorBodyChars+1, len([]rune(got)))
	}

	// Multi-byte truncation must cut on a rune boundary: a byte-wise slice would
	// emit an invalid string that reaches the log garbage-escaped.
	got = truncateErrorBody([]byte(strings.Repeat("中", 300)))
	if !utf8.ValidString(got) {
		t.Errorf("truncation produced invalid UTF-8: %q", got)
	}
}

// The redactor must not be defeated by the shapes real upstreams produce: a
// pretty-printed `"userId" : "..."`, a value of a non-string type, a value whose
// closing quote is missing because the response was cut off, and a key that only
// appears inside a string value (which must NOT be redacted).
func TestTruncateErrorBodyRedactionShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"compact", `{"userId":"u-42","error":"down"}`},
		{"space before colon", `{"userId" : "u-42", "error":"down"}`},
		{"tab after colon", "{\"userId\":\t\"u-42\"}"},
		{"pretty printed", "{\n  \"userId\": \"u-42\",\n  \"traceId\": \"t-99\"\n}"},
		{"nested object", `{"data":{"userId":"u-42"}}`},
		{"numeric value", `{"userId":12345,"error":"down"}`},
		{"boolean value", `{"userId":true}`},
		{"unterminated value", `{"userId":"u-42`},
		{"escaped quote in value", `{"userId":"u\"42","error":"down"}`},
		{"repeated key", `{"userId":"u-42","x":{"userId":"u-43"}}`},
	}
	secrets := []string{"u-42", "u-43", "t-99", "12345", "true"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateErrorBody([]byte(tc.body))
			for _, secret := range secrets {
				if strings.Contains(got, secret) {
					t.Errorf("identity value %q survived redaction: %q", secret, got)
				}
			}
		})
	}

	// A key occurring only inside a string value is not a field assignment and
	// must be left alone; over-redacting would hide the upstream's message.
	got := truncateErrorBody([]byte(`{"msg":"see userId for details","error":"down"}`))
	if !strings.Contains(got, "see userId for details") {
		t.Errorf("a key inside a string value must not be treated as a field: %q", got)
	}
	// A body that is not JSON at all must survive intact.
	if got := truncateErrorBody([]byte("<html>oops</html>")); got != "<html>oops</html>" {
		t.Errorf("non-JSON body mangled: %q", got)
	}
}

// Every non-200 path that embeds the upstream body in an error must route it
// through truncateErrorBody: the bridge logs these errors, and the bodies carry
// userId / trace ids. The response body is generated with the identity first so
// truncation alone would not hide it.
func TestNon200ErrorsRedactBody(t *testing.T) {
	const userID = "secret-user-id-42"
	body := `{"userId":"` + userID + `","message":"` + strings.Repeat("x", 400) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, body)
	}))
	defer srv.Close()

	assertRedacted := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		msg := err.Error()
		if !strings.Contains(msg, "500") {
			t.Errorf("%s: expected the HTTP status, got %q", name, msg)
		}
		if strings.Contains(msg, userID) {
			t.Errorf("%s: the error leaked the identity: %q", name, msg)
		}
		if len(msg) > 400 {
			t.Errorf("%s: body was not truncated: %d chars", name, len(msg))
		}
	}

	_, err := call(context.Background(), testSession(), "GET", srv.URL, nil, nil)
	assertRedacted("call", err)

	_, err = postEncoded(context.Background(), srv.URL, map[string]interface{}{"a": 1}, "mid", "mtok", "mtype")
	assertRedacted("postEncoded", err)

	err = OpenStreamLines(context.Background(), testSession(), srv.URL, []byte(`{}`), nil, func(string) error { return nil })
	assertRedacted("OpenStreamLines", err)
}

// AuthError.Detail holds the raw upstream body, and AuthError.Error() is what the
// bridge logs ("auth error before any content"). The redaction must therefore
// happen in Error(), covering all four construction sites at once, while Detail
// stays raw for callers that classify the failure.
func TestAuthErrorRedactsDetailInErrorString(t *testing.T) {
	const userID = "secret-user-id-42"
	fresh := &AuthError{StatusCode: 401, Detail: `{"userId":"` + userID + `","message":"token expired"}`}

	msg := fresh.Error()
	if strings.Contains(msg, userID) {
		t.Errorf("AuthError.Error leaked the identity: %q", msg)
	}
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "token expired") {
		t.Errorf("AuthError.Error lost its diagnostic signal: %q", msg)
	}
	if !strings.Contains(fresh.Detail, userID) {
		t.Errorf("Detail must stay raw for classification, got %q", fresh.Detail)
	}

	// A plain HTTP status error must not gain a trailing space when Detail is empty.
	if got := (&AuthError{StatusCode: 500}).Error(); got != "HTTP 500" {
		t.Errorf("expected %q, got %q", "HTTP 500", got)
	}
}

// ParseAccountStatus is the single conversion point between the raw
// /user/status payload and the account metadata the recorder persists, so its
// accept/reject rule and field coercion are locked here (§28).
func TestParseAccountStatus(t *testing.T) {
	full := map[string]interface{}{
		"userType":        "teams",
		"plan":            "PLAN_TIER_TEAM",
		"userTag":         "Teams",
		"orgName":         "Acme",
		"nextResetAt":     float64(1784194052563),
		"isQuotaExceeded": true,
	}
	st, ok := ParseAccountStatus(full)
	if !ok {
		t.Fatal("expected a fully populated payload to be accepted")
	}
	if st.UserType != "teams" || st.Plan != "PLAN_TIER_TEAM" || st.UserTag != "Teams" || st.OrgName != "Acme" {
		t.Errorf("unexpected identity fields: %+v", st)
	}
	if st.NextResetAtMs != 1784194052563 {
		t.Errorf("nextResetAt = %d, want 1784194052563", st.NextResetAtMs)
	}
	if !st.IsQuotaExceeded {
		t.Error("expected isQuotaExceeded to be carried through")
	}

	// A nil payload and one with no recognizable field are both rejections.
	if _, ok := ParseAccountStatus(nil); ok {
		t.Error("nil payload must be rejected")
	}
	if _, ok := ParseAccountStatus(map[string]interface{}{"somethingElse": 1}); ok {
		t.Error("a payload with no account field must be rejected")
	}
	// Identity-only payloads are accepted, with the reset instant left at 0:
	// the bridge relies on that 0 to mean "no boundary reported".
	st, ok = ParseAccountStatus(map[string]interface{}{"userType": "personal_standard"})
	if !ok || st.UserType != "personal_standard" || st.NextResetAtMs != 0 {
		t.Errorf("identity-only payload: ok=%v st=%+v", ok, st)
	}
	// nextResetAt alone is enough to accept, even without userType/plan.
	if _, ok := ParseAccountStatus(map[string]interface{}{"nextResetAt": float64(1)}); !ok {
		t.Error("a payload carrying only nextResetAt must be accepted")
	}
}

// toInt64Ms must only accept the float64 shape encoding/json produces. The
// gateway currently returns nextResetAt as a JSON number; a string form is a
// silent 0 (documented in the review as §10.6), which must not panic or fabricate
// a boundary.
func TestToInt64Ms(t *testing.T) {
	cases := []struct {
		name string
		in   interface{}
		want int64
	}{
		{"json number", float64(1784194052563), 1784194052563},
		{"zero", float64(0), 0},
		{"nil", nil, 0},
		{"string", "1784194052563", 0},
		{"bool", true, 0},
		{"object", map[string]interface{}{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toInt64Ms(tc.in); got != tc.want {
				t.Errorf("toInt64Ms(%#v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// The encoded /user/status request body is decoded by the gateway against a
// positional-ish contract: the field names and the fact that authInfo is an empty
// JSON object must not drift, because the mismatch only surfaces as a live 401
// rather than a build failure (§28).
func TestFetchUserStatusRequestBodyContract(t *testing.T) {
	var gotURL string
	var gotInner string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		// The outer body is an Encode()d blob wrapping
		// {"payload":"<inner JSON>","encodeVersion":"1"}.
		plain, err := Decode(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Errorf("request body is not Encode()d: %v", err)
			return
		}
		var outer struct {
			Payload       string `json:"payload"`
			EncodeVersion string `json:"encodeVersion"`
		}
		if err := json.Unmarshal(plain, &outer); err != nil {
			t.Errorf("outer payload: %v", err)
			return
		}
		gotInner = outer.Payload
		if outer.EncodeVersion != "1" {
			t.Errorf("encodeVersion = %q, want %q", outer.EncodeVersion, "1")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"userType":"teams","plan":"PLAN_TIER_TEAM"}`)
	}))
	defer srv.Close()

	if _, err := FetchUserStatus(context.Background(), "uid-42", "mid", "mtok", "default", &RegionConfig{AuthBase: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if gotURL != "/algo/api/v3/user/status" {
		t.Errorf("path = %q, want /algo/api/v3/user/status", gotURL)
	}
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(gotInner), &fields); err != nil {
		t.Fatal(err)
	}
	if fields["userId"] != "uid-42" {
		t.Errorf("userId = %v, want uid-42", fields["userId"])
	}
	if fields["needRefresh"] != false {
		t.Errorf("needRefresh = %v, want false", fields["needRefresh"])
	}
	// authInfo must serialize as an empty object, not null: the gateway rejects
	// the request otherwise.
	if authInfo, present := fields["authInfo"]; !present || authInfo == nil {
		t.Errorf("authInfo = %#v, want an empty object", fields["authInfo"])
	} else if m, isMap := authInfo.(map[string]interface{}); !isMap || len(m) != 0 {
		t.Errorf("authInfo = %#v, want {}", authInfo)
	}
}
