package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCheckinTestSession builds a minimal session good enough for the check-in
// path (only the identity token and machine fields are read).
func newCheckinTestSession() *SessionContext {
	return &SessionContext{
		Identity:     AuthIdentity{SecurityOauthToken: "sot-token"},
		MachineID:    "mid",
		MachineToken: "mtok",
		MachineType:  "mtype",
	}
}

// withCheckinServer points the endpoint hook at srv for the test duration.
func withCheckinServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := checkinEndpointHook
	checkinEndpointHook = func(*RegionConfig) (string, string) { return srv.URL, "https://origin.test" }
	t.Cleanup(func() { checkinEndpointHook = old })
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestCheckinSuccess(t *testing.T) {
	var claimed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Cosy-MachineCode") == "" {
			t.Error("missing Cosy-MachineCode")
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/campaigns"):
			status := "CLAIMABLE"
			if claimed {
				status = "CLAIMED"
			}
			writeJSON(w, map[string]any{"campaigns": []map[string]any{{
				"campaignId": "c1", "actionType": "CLAIM_BENEFIT", "claimStatus": status,
				"benefit": map[string]any{"kind": "CREDITS", "amount": 10},
			}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/c1/claim"):
			claimed = true
			writeJSON(w, map[string]any{"status": "CLAIMED"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	res, err := Checkin(context.Background(), newCheckinTestSession(), "pat-1", CN)
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if res.RewardCredits != 10 {
		t.Errorf("reward = %v, want 10", res.RewardCredits)
	}
}

func TestCheckinAlready(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"campaigns": []map[string]any{{
			"campaignId": "c1", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"benefit": map[string]any{"kind": "CREDITS", "amount": 10},
		}}})
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	res, err := Checkin(context.Background(), newCheckinTestSession(), "pat-1", CN)
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if res.Status != "already" {
		t.Fatalf("status = %q, want already", res.Status)
	}
}

func TestCheckinSkippedOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	res, err := Checkin(context.Background(), newCheckinTestSession(), "pat-1", CN)
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if res.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", res.Status)
	}
}

// A non-credit CLAIM_BENEFIT (e.g. a REDEMPTION_CODE voucher) must NOT be
// treated as a daily check-in: with no credit campaign present the result is
// "skipped", never "already".
func TestCheckinNonCreditClaimIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"campaigns": []map[string]any{{
			"campaignId": "voucher", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"benefit": map[string]any{"kind": "REDEMPTION_CODE", "amount": 1},
		}}})
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	res, err := Checkin(context.Background(), newCheckinTestSession(), "pat-1", CN)
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if res.Status != "skipped" {
		t.Fatalf("status = %q, want skipped (voucher is not a daily check-in)", res.Status)
	}
}

// A claim that returns a non-CLAIMED status but shows CLAIMED on re-list is
// counted as recovered (asynchronous confirmation).
func TestCheckinClaimRecovered(t *testing.T) {
	claimCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			// First list: claimable. Later lists: claimed.
			status := "CLAIMABLE"
			if claimCalls > 0 {
				status = "CLAIMED"
			}
			writeJSON(w, map[string]any{"campaigns": []map[string]any{{
				"campaignId": "c1", "actionType": "CLAIM_BENEFIT", "claimStatus": status,
				"benefit": map[string]any{"kind": "CREDITS", "amount": 100},
			}}})
		case r.Method == http.MethodPost:
			claimCalls++
			writeJSON(w, map[string]any{"status": "PENDING"})
		}
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	res, err := Checkin(context.Background(), newCheckinTestSession(), "pat-1", CN)
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}
	if res.Status != "already" {
		t.Fatalf("status = %q, want already (recovered)", res.Status)
	}
}

// CheckinWithRisk must send the supplied genuine device fingerprint headers
// instead of the PAT-derived ones.
func TestCheckinWithRiskSendsRiskHeaders(t *testing.T) {
	var gotToken, gotType, gotCode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Cosy-MachineToken")
		gotType = r.Header.Get("Cosy-MachineType")
		gotCode = r.Header.Get("Cosy-MachineCode")
		writeJSON(w, map[string]any{"campaigns": []map[string]any{{
			"campaignId": "c1", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"benefit": map[string]any{"kind": "CREDITS", "amount": 100},
		}}})
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	risk := &RiskIdentity{MachineToken: "real-token-88", MachineType: "real-type", MachineCode: "real-code"}
	if _, err := CheckinWithRisk(context.Background(), newCheckinTestSession(), "pat-1", CN, risk); err != nil {
		t.Fatalf("CheckinWithRisk: %v", err)
	}
	if gotToken != "real-token-88" || gotType != "real-type" || gotCode != "real-code" {
		t.Errorf("risk headers not sent: token=%q type=%q code=%q", gotToken, gotType, gotCode)
	}
}

// A nil risk identity falls back to the derived headers (non-empty token).
func TestCheckinWithRiskNilFallsBack(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Cosy-MachineToken")
		writeJSON(w, map[string]any{"campaigns": []map[string]any{{
			"campaignId": "c1", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"benefit": map[string]any{"kind": "CREDITS", "amount": 100},
		}}})
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	if _, err := CheckinWithRisk(context.Background(), newCheckinTestSession(), "pat-1", CN, nil); err != nil {
		t.Fatalf("CheckinWithRisk: %v", err)
	}
	if gotToken != "mtok" { // newCheckinTestSession's MachineToken
		t.Errorf("expected fallback to session machine token, got %q", gotToken)
	}
}

// CheckinWithBearer must use the explicitly supplied bearer (e.g. the OpenAPI
// token for global) rather than the session's securityOauthToken.
func TestCheckinWithBearerUsesSuppliedToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeJSON(w, map[string]any{"campaigns": []map[string]any{{
			"campaignId": "c1", "actionType": "CLAIM_BENEFIT", "claimStatus": "CLAIMED",
			"benefit": map[string]any{"kind": "CREDITS", "amount": 100},
		}}})
	}))
	defer srv.Close()
	withCheckinServer(t, srv)

	sess := newCheckinTestSession() // securityOauthToken = "sot-token"
	if _, err := CheckinWithBearer(context.Background(), sess, "pat-1", CN, "openapi-bearer-xyz", nil); err != nil {
		t.Fatalf("CheckinWithBearer: %v", err)
	}
	if gotAuth != "Bearer openapi-bearer-xyz" {
		t.Errorf("bearer = %q, want the supplied openapi token", gotAuth)
	}
}
