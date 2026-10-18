package auth

// Daily credit check-in against the Qoder OpenAPI campaigns endpoint.
//
// The flow mirrors the desktop client (and cli2api's in-process implementation):
//
//	GET  {openapi}/sash/api/v1/me/campaigns            → list today's campaigns
//	POST {openapi}/sash/api/v1/me/campaigns/{id}/claim → claim one campaign
//
// A campaign is claimable when actionType == "CLAIM_BENEFIT" and
// claimStatus == "CLAIMABLE"; an already-CLAIMED campaign means "今日已签到".
// The request authenticates with the session's securityOauthToken as a Bearer
// token (not the COSY-signed bearer used by the chat path) plus the machine
// identity headers, so the device fingerprint matches the one used to bootstrap
// the session.

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

// CheckinResult is the outcome of one daily check-in attempt.
//
// Status is one of:
//
//	success — at least one campaign was claimed this call
//	already — everything was already claimed today ("今日已签到")
//	skipped — no credit campaign is open today
//
// An actual failure is returned as an error, not a status.
type CheckinResult struct {
	Status        string  `json:"status"`
	Message       string  `json:"message"`
	RewardCredits float64 `json:"reward_credits,omitempty"`
}

// Valid reports whether the status is one of the accepted outcomes.
func (r CheckinResult) Valid() bool {
	switch r.Status {
	case "success", "already", "skipped":
		return true
	default:
		return false
	}
}

const checkinCampaignsPath = "/sash/api/v1/me/campaigns"

// checkinHTTPClient is dedicated to OpenAPI check-in calls so a slow campaigns
// endpoint does not pin a chat-stream transport.
var checkinHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		// redirect: manual — the campaigns endpoint 302s to a login page when the
		// token is rejected, and we want to see the 302 rather than follow it.
		return http.ErrUseLastResponse
	},
}

// CheckinHTTPError carries the upstream HTTP status so the caller can map a 404
// (activity not open today) to a clean "skipped" result and a 401 to a token
// refresh + retry. The body is already truncated for safe logging.
type CheckinHTTPError struct {
	Code int
	Body string
}

func (e *CheckinHTTPError) Error() string {
	return fmt.Sprintf("qoder check-in: HTTP %d: %s", e.Code, e.Body)
}

// StatusCode lets errors.As(&someErr, &checkinHTTPError) feed the 404/401 paths.
func (e *CheckinHTTPError) StatusCode() int { return e.Code }

// checkinEndpointHook overrides the OpenAPI endpoint in tests. Production
// leaves it nil so the real region-configured endpoint is used.
var checkinEndpointHook func(region *RegionConfig) (base, origin string)

// checkinEndpoint resolves the OpenAPI base and browser Origin for a region.
func checkinEndpoint(region *RegionConfig) (base, origin string) {
	if checkinEndpointHook != nil {
		return checkinEndpointHook(region)
	}
	if region != nil && region.Name == "global" {
		return "https://openapi.qoder.sh", "https://qoder.sh"
	}
	return "https://openapi.qoder.com.cn", "https://qoder.com.cn"
}

// Checkin performs the daily credit-claim using the supplied session. The
// caller is responsible for ensuring the session is fresh; a 401 returned here
// is surfaced as *CheckinHTTPError so the bridge can force-refresh and retry.
//
// This variant sends the PAT-derived device fingerprint. Use CheckinWithRisk to
// send the genuine desktop fingerprint, which the campaign service requires to
// expose the daily credit activity.
func Checkin(ctx context.Context, sess *SessionContext, pat string, region *RegionConfig) (CheckinResult, error) {
	return CheckinWithRisk(ctx, sess, pat, region, nil)
}

// CheckinWithRisk is Checkin with an optional genuine device fingerprint. When
// risk is valid, its machineToken/Type/Code replace the PAT-derived values, so
// the campaign service treats the request as the official desktop client and
// includes the claimable daily credit activity. When risk is nil or partial,
// the derived identity is used instead.
//
// The Bearer token defaults to the session's securityOauthToken. Global
// (qoder.sh) rejects that token on the campaign endpoint (401 TOKEN_EXPIRE) and
// requires the OpenAPI bearer instead; callers there should use
// CheckinWithBearer with the OpenAPI token.
func CheckinWithRisk(ctx context.Context, sess *SessionContext, pat string, region *RegionConfig, risk *RiskIdentity) (CheckinResult, error) {
	if sess == nil {
		return CheckinResult{}, errors.New("qoder check-in: no session")
	}
	return CheckinWithBearer(ctx, sess, pat, region, sess.Identity.SecurityOauthToken, risk)
}

// CheckinWithBearer is the low-level entry: it uses the supplied bearer token
// (CN: securityOauthToken; global: OpenAPI token) plus the optional genuine
// device fingerprint.
func CheckinWithBearer(ctx context.Context, sess *SessionContext, pat string, region *RegionConfig, bearer string, risk *RiskIdentity) (CheckinResult, error) {
	if sess == nil {
		return CheckinResult{}, errors.New("qoder check-in: no session")
	}
	token := strings.TrimSpace(bearer)
	if token == "" {
		return CheckinResult{}, errors.New("qoder check-in: token unavailable")
	}

	base, origin := checkinEndpoint(region)

	// Prefer the genuine desktop fingerprint when available: the campaign
	// service only exposes the claimable daily-credit activity to the official
	// client's machine identity. Fall back to the PAT-derived identity otherwise.
	machineID := sess.MachineID
	machineToken := sess.MachineToken
	machineType := sess.MachineType
	machineCode := deriveMachineCode(pat)
	if risk.Valid() {
		machineToken = risk.MachineToken
		machineType = risk.MachineType
		machineCode = risk.MachineCode
	}
	machineOS := machineOSString()
	machineHostname := machineHostnameString()

	doRequest := func(path, method string) (map[string]any, int, error) {
		reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, method, base+path, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Qoder")
		req.Header.Set("Cosy-ClientType", "10")
		req.Header.Set("Cosy-Version", "0.3.4")
		req.Header.Set("Cosy-MachineId", machineID)
		req.Header.Set("Cosy-MachineToken", machineToken)
		req.Header.Set("Cosy-MachineType", machineType)
		req.Header.Set("Cosy-MachineCode", machineCode)
		if machineOS != "" {
			req.Header.Set("Cosy-MachineOS", machineOS)
		}
		if machineHostname != "" {
			req.Header.Set("Cosy-MachineHostname", machineHostname)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", base+"/growth-page/activity-iframe")
		resp, err := checkinHTTPClient.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("qoder check-in: request failed: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode >= 300 {
			return nil, resp.StatusCode, &CheckinHTTPError{Code: resp.StatusCode, Body: truncateOneLine(string(body))}
		}
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("qoder check-in: invalid JSON: %w", err)
		}
		return parsed, resp.StatusCode, nil
	}

	// 1) List campaigns.
	listRaw, _, err := doRequest(checkinCampaignsPath, http.MethodGet)
	if err != nil {
		// 404 means the credit campaign is not open today; surface as skipped
		// rather than an error so the scheduler logs a clean "already/none".
		var statusErr interface{ StatusCode() int }
		if errors.As(err, &statusErr) && statusErr.StatusCode() == http.StatusNotFound {
			return CheckinResult{Status: "skipped", Message: "暂无可签到活动"}, nil
		}
		return CheckinResult{}, err
	}
	items := campaignsFrom(listRaw)
	benefits := creditCampaigns(items)
	if len(benefits) == 0 {
		return CheckinResult{Status: "skipped", Message: "暂无可签到活动"}, nil
	}
	claimable := claimableCampaigns(benefits)
	if len(claimable) == 0 {
		for _, c := range benefits {
			if c.ClaimStatus == "CLAIMED" {
				return CheckinResult{Status: "already", Message: "今日已签到"}, nil
			}
		}
		return CheckinResult{Status: "skipped", Message: "暂无可签到活动"}, nil
	}

	// 2) Claim each claimable campaign. A claim that returns a non-CLAIMED status
	// is retried by re-listing and checking the campaign's status: the gateway
	// sometimes confirms the claim asynchronously.
	confirmed := 0
	recovered := 0
	reward := 0.0
	hasReward := false
	for _, campaign := range claimable {
		path := fmt.Sprintf("%s/%s/claim", checkinCampaignsPath, url.PathEscape(campaign.CampaignID))
		claimRaw, _, err := doRequest(path, http.MethodPost)
		if err != nil {
			// Re-list to see if the claim actually succeeded despite the error.
			relist, _, relistErr := doRequest(checkinCampaignsPath, http.MethodGet)
			if relistErr != nil {
				return CheckinResult{}, err
			}
			if status := claimStatusOf(campaignsFrom(relist), campaign.CampaignID); status == "CLAIMED" {
				recovered++
			} else {
				return CheckinResult{}, err
			}
		} else {
			status := unwrapClaimStatus(claimRaw)
			if status != "CLAIMED" {
				// Fall back to re-listing; the claim may have been recorded server-side.
				relist, _, relistErr := doRequest(checkinCampaignsPath, http.MethodGet)
				if relistErr != nil {
					return CheckinResult{}, fmt.Errorf("qoder check-in: claim unconfirmed")
				}
				if status := claimStatusOf(campaignsFrom(relist), campaign.CampaignID); status == "CLAIMED" {
					recovered++
				} else {
					return CheckinResult{}, fmt.Errorf("qoder check-in: claim unconfirmed")
				}
			} else {
				confirmed++
			}
		}
		if amount, ok := rewardFrom(campaign); ok {
			hasReward = true
			reward += amount
		}
	}
	if confirmed == 0 && recovered == 0 {
		return CheckinResult{}, fmt.Errorf("qoder check-in: claim unconfirmed")
	}
	if confirmed == 0 {
		return CheckinResult{Status: "already", Message: "已签到（复查确认）"}, nil
	}
	out := CheckinResult{Status: "success"}
	if hasReward {
		out.Message = fmt.Sprintf("签到成功 +%g 积分", reward)
		out.RewardCredits = reward
	} else {
		out.Message = "签到成功"
	}
	return out, nil
}

// campaign is a single entry in the /me/campaigns response.
type campaign struct {
	CampaignID  string `json:"campaignId"`
	ActionType  string `json:"actionType"`
	ClaimStatus string `json:"claimStatus"`
	Benefit     struct {
		Kind   string  `json:"kind"`
		Amount float64 `json:"amount"`
	} `json:"benefit"`
}

func campaignsFrom(raw map[string]any) []campaign {
	if raw == nil {
		return nil
	}
	var list []any
	if v, ok := raw["campaigns"].([]any); ok {
		list = v
	} else if data, ok := raw["data"].(map[string]any); ok {
		if c, ok := data["campaigns"].([]any); ok {
			list = c
		}
	} else if v, ok := raw["data"].([]any); ok {
		list = v
	}
	out := make([]campaign, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		encoded, _ := json.Marshal(m)
		var c campaign
		_ = json.Unmarshal(encoded, &c)
		out = append(out, c)
	}
	return out
}

// creditCampaigns returns the daily check-in campaigns: CLAIM_BENEFIT entries
// whose benefit is CREDITS. This deliberately excludes other CLAIM_BENEFIT
// activities that grant non-credit perks (e.g. a REDEMPTION_CODE milk-tea
// voucher), which must not be reported as "already checked in today".
func creditCampaigns(items []campaign) []campaign {
	out := make([]campaign, 0, len(items))
	for _, c := range items {
		if c.ActionType != "CLAIM_BENEFIT" || strings.TrimSpace(c.CampaignID) == "" {
			continue
		}
		if c.Benefit.Kind != "CREDITS" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func claimableCampaigns(items []campaign) []campaign {
	out := make([]campaign, 0, len(items))
	for _, c := range items {
		if c.ClaimStatus == "CLAIMABLE" {
			out = append(out, c)
		}
	}
	return out
}

func claimStatusOf(items []campaign, id string) string {
	for _, c := range items {
		if c.CampaignID == id {
			return c.ClaimStatus
		}
	}
	return ""
}

func unwrapClaimStatus(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	if s, ok := raw["status"].(string); ok {
		return s
	}
	if data, ok := raw["data"].(map[string]any); ok {
		if s, ok := data["status"].(string); ok {
			return s
		}
	}
	return ""
}

func rewardFrom(c campaign) (float64, bool) {
	if c.Benefit.Kind != "CREDITS" {
		return 0, false
	}
	if c.Benefit.Amount < 0 {
		return 0, false
	}
	return c.Benefit.Amount, true
}

// truncateOneLine collapses a response body to a single short line for error
// messages; the campaigns endpoint returns JSON with embedded user ids that
// must not reach the log verbatim.
func truncateOneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// deriveMachineCode is the fourth machine-identity header the OpenAPI campaigns
// endpoint expects (Cosy-MachineCode), alongside Id/Token/Type. The CLI computes
// it from real hardware; we derive a stable value from the PAT instead, mirroring
// the other derived identities. A partial identity (missing any of the four)
// causes the campaigns endpoint to silently drop the claimable credit campaign,
// so this MUST be non-empty.
func deriveMachineCode(pat string) string {
	sum := md5.Sum([]byte("machinecode:" + pat))
	return hex.EncodeToString(sum[:])
}

// machineOSString mirrors the desktop client's machineOS(): "<arch>_<platform>"
// (e.g. "aarch64_darwin").
func machineOSString() string {
	arch := "x86_64"
	switch runtime.GOARCH {
	case "arm64":
		arch = "aarch64"
	case "amd64":
		arch = "x86_64"
	}
	return arch + "_" + runtime.GOOS
}

// machineHostnameString returns the host name if it is printable ASCII (≤96
// bytes), otherwise a sha256-prefixed sanitized form. Empty when neither form is
// producible.
func machineHostnameString() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return ""
	}
	trimmed := strings.TrimSpace(name)
	if len([]byte(trimmed)) > 96 {
		return ""
	}
	if isPrintableASCII(trimmed) {
		return trimmed
	}
	// Fall back to a sanitized form for non-ASCII hostnames (CJK laptops).
	safe := strings.Map(func(r rune) rune {
		if r < 0x21 || r > 0x7e {
			return '-'
		}
		return r
	}, trimmed)
	safe = strings.ReplaceAll(safe, "--", "-")
	safe = strings.Trim(safe, "-")
	if len(safe) > 87 {
		safe = safe[:87]
	}
	if safe == "" {
		return ""
	}
	digest := shortHash(trimmed)
	out := safe + "-" + digest
	out = strings.TrimRight(out, "-")
	if !isPrintableASCII(out) {
		return ""
	}
	return out
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return len(s) > 0
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
