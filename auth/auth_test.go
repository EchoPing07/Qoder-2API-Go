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
