package auth

import (
	"bytes"
	"encoding/base64"
	"testing"
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
