// Session lifecycle for a single PAT: cold bootstrap, proactive/forced
// renewal, account (subscription) status resolution, and the dynamic model
// catalog. Extracted from bridge.go; behaviour is unchanged.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"qoder2api/auth"
	"qoder2api/models"
)

var (
	// refreshMarginMs renews the session this long before the reported expiry.
	refreshMarginMs = int64(2 * 3600 * 1000) // 2 hours
	// catalogTTL bounds how long a fetched model/list response is trusted.
	catalogTTL = float64(600) // 10 minutes
	// accountTTL bounds how long a cached /user/status result is trusted.
	// The subscription tier effectively never changes and the billing
	// boundary moves once a month, so an hour is far tighter than needed
	// while keeping the status endpoint off the chat path entirely.
	accountTTL = float64(3600)
	// accountStatusTimeout bounds the /user/status lookup performed by
	// resolveAccountStatus. That lookup runs while refreshMu is held
	// (bootstrapSession / doRenew), so a slow status endpoint would serialize
	// every queued chat request behind it. The lookup is best-effort
	// enrichment only — the cached/last-known tier already covers the failure
	// case — so it must not hold the session lock for the full 15s request
	// timeout.
	accountStatusTimeout = 5 * time.Second
)

// maxPlausibleResetMs bounds a believable subscription boundary. The OpenAPI
// quota endpoint reports 9999-12-31 (253402214400000) as a "never expires"
// sentinel for plans without an expiry; that value must not be mistaken for
// the instant the allowance actually refreshes.
const maxPlausibleResetMs = int64(4102444800000) // 2100-01-01T00:00:00Z

// plausibleResetMs reports whether ms is a real subscription boundary rather
// than a missing value or the far-future sentinel.
func plausibleResetMs(ms int64) bool {
	return ms > 0 && ms < maxPlausibleResetMs
}

// unknownExpiryTTL bounds how long a session with a missing/invalid
// expireTime is trusted. Without it, expireTimeMs == 0 makes needsRefresh
// always true, degrading every request to a serialized renewal call.
const unknownExpiryTTL = 30 * time.Minute

// bootstrapSession performs a cold PAT → jobToken exchange.
func (b *OpenAiBridge) bootstrapSession(ctx context.Context) error {
	jt, err := auth.ExchangeJobToken(ctx, b.pat, b.machineID, b.machineToken, b.machineType, b.Region)
	if err != nil {
		return err
	}
	id, _ := jt["id"].(string)
	exp, _ := jt["expireTime"]
	// The jobToken response carries no userType, so the authoritative tier has
	// to come from /user/status. It must be resolved BEFORE the session is
	// built because the value is AES-signed into the bearer payload and cannot
	// be amended afterwards.
	userType := b.resolveAccountStatus(ctx, id)
	name, _ := jt["name"].(string)
	log.Printf("[bridge] session for %s (%s) [%s] exp=%v userType=%s", name, id, b.Region.Name, exp, userType)
	b.applyJobToken(jt, userType)
	b.bootstrapped.Store(true)
	return nil
}

// resolveAccountStatus fetches /user/status and returns the authoritative
// subscription tier. The lookup is bounded by accountStatusTimeout because it
// runs under refreshMu (bootstrap/renewal): it is best-effort enrichment, and
// the last known tier already covers the failure case. On failure it degrades
// in order: last known tier (even if stale) → empty, letting applyJobToken
// fall back to the jobToken value and then the historical default.
//
// Preferring a stale tier over the default matters on the renewal path: a
// transient status outage must not downgrade a team account to
// personal_standard and change the signed bearer payload mid-session.
func (b *OpenAiBridge) resolveAccountStatus(ctx context.Context, userID string) string {
	// Best-effort enrichment with a bounded deadline: this runs under
	// refreshMu, so it gets its own short context instead of inheriting the
	// caller's (potentially much longer) one. See accountStatusTimeout.
	lookupCtx, cancel := context.WithTimeout(ctx, accountStatusTimeout)
	defer cancel()
	st, err := b.fetchAccountStatus(lookupCtx, userID)
	if err == nil && st != nil && st.UserType != "" {
		return st.UserType
	}
	// errNoUserID is the expected degradation of the first jobToken exchange
	// (which carries no user id), not a failure: fall back silently. Genuine
	// lookup failures still deserve the WARN.
	if err != nil && !errors.Is(err, errNoUserID) {
		log.Printf("[bridge] WARN user/status failed (%v); reusing last known tier", err)
	}
	if cached := b.AccountStatus(); cached != nil && cached.UserType != "" {
		return cached.UserType
	}
	return ""
}

// errNoUserID marks the expected degradation where the first jobToken
// exchange does not carry a user id and /user/status cannot be queried yet.
// Callers match it with errors.Is to skip the WARN that genuine failures get.
var errNoUserID = errors.New("no user id available for user/status")

// fetchAccountStatus queries /user/status with a TTL cache. Unlike the chat
// path it performs no single-flight coordination: callers are the renewal
// flows (already serialized by refreshMu) and the admin poll.
func (b *OpenAiBridge) fetchAccountStatus(ctx context.Context, userID string) (*auth.AccountStatus, error) {
	now := float64(time.Now().Unix())
	b.accountMu.Lock()
	if cached := b.account; cached != nil && now-b.accountTs < accountTTL {
		b.accountMu.Unlock()
		// Every return path hands out a clone: exposing the internal pointer
		// would let a mutating caller rewrite the cache without accountMu.
		return cloneAccountStatus(cached), nil
	}
	b.accountMu.Unlock()

	if userID == "" {
		return nil, errNoUserID
	}
	raw, err := auth.FetchUserStatus(ctx, userID, b.machineID, b.machineToken, b.machineType, b.Region)
	if err != nil {
		return nil, err
	}
	st, ok := auth.ParseAccountStatus(raw)
	if !ok {
		return nil, fmt.Errorf("user/status response shape unexpected")
	}
	b.accountMu.Lock()
	// A successful identity refresh must not erase the last authoritative
	// quota snapshot if the immediately following OpenAPI call fails. Preserve
	// only quota-owned fields; plan/tag/org metadata comes from this fresh
	// gateway response.
	if previous := b.account; previous != nil && previous.UserQuota != nil {
		st.NextResetAtMs = previous.NextResetAtMs
		st.IsQuotaExceeded = previous.IsQuotaExceeded
		st.TotalUsagePercentage = previous.TotalUsagePercentage
		st.UserQuota = previous.UserQuota
		st.AddOnQuota = previous.AddOnQuota
		st.OrgResourcePackage = previous.OrgResourcePackage
	}
	b.account = cloneAccountStatus(&st)
	b.accountTs = float64(time.Now().Unix())
	b.accountMu.Unlock()
	log.Printf("[bridge] account status: userType=%s plan=%s tag=%s nextReset=%s quotaExceeded=%t",
		st.UserType, st.Plan, st.UserTag, fmtResetTime(st.NextResetAtMs), st.IsQuotaExceeded)
	return &st, nil
}

// EnsureAccountStatus returns the subscription state, refreshing both the
// gateway identity metadata and the authoritative OpenAPI quota snapshot. It
// bootstraps the session first because OpenAPI uses the securityOauthToken
// produced by that exchange. Failures preserve the last known snapshot so a
// temporary account-service outage never breaks the caller's accounting loop.
func (b *OpenAiBridge) EnsureAccountStatus(ctx context.Context) *auth.AccountStatus {
	if err := b.EnsureFreshSession(ctx); err != nil {
		log.Printf("[bridge] WARN account status unavailable (session: %v)", err)
		return b.AccountStatus()
	}
	userID := ""
	if ident := b.currentIdentity(); ident != nil {
		userID = ident.Aid
	}
	st, err := b.fetchAccountStatus(ctx, userID)
	if err != nil {
		log.Printf("[bridge] WARN user/status refresh failed: %v", err)
		st = b.AccountStatus()
	}

	quota, err := auth.FetchQuotaUsage(ctx, b.currentSecurityOauth(), b.Region)
	if err != nil {
		log.Printf("[bridge] WARN quota/usage refresh failed: %v", err)
		return st
	}
	if st == nil {
		st = &auth.AccountStatus{}
	} else {
		st = cloneAccountStatus(st)
	}
	if quota.UserType != "" {
		st.UserType = quota.UserType
	}
	// Boundary precedence: the gateway's nextResetAt is authoritative, and the
	// OpenAPI expiresAt is only a fallback for when it is missing. Plans without
	// an expiry report the far-future 9999-12-31 sentinel in expiresAt, which
	// must never be mistaken for the instant the allowance actually refreshes.
	if !plausibleResetMs(st.NextResetAtMs) {
		st.NextResetAtMs = 0
	}
	if st.NextResetAtMs == 0 && plausibleResetMs(quota.ExpiresAtMs) {
		st.NextResetAtMs = quota.ExpiresAtMs
	}
	st.IsQuotaExceeded = quota.IsQuotaExceeded
	st.TotalUsagePercentage = quota.TotalUsagePercentage
	st.UserQuota = quota.UserQuota
	st.AddOnQuota = quota.AddOnQuota
	st.OrgResourcePackage = quota.OrgResourcePackage

	b.accountMu.Lock()
	b.account = cloneAccountStatus(st)
	b.accountMu.Unlock()
	log.Printf("[bridge] quota usage: userType=%s used=%.2f total=%.2f remaining=%.2f reset=%s quotaExceeded=%t",
		st.UserType, st.UserQuota.Used, st.UserQuota.Total, st.UserQuota.Remaining,
		fmtResetTime(st.NextResetAtMs), st.IsQuotaExceeded)
	return cloneAccountStatus(st)
}

// AccountStatus returns the cached subscription state, or nil when it has
// never been resolved. It performs no network call, so it is safe to invoke
// from a request handler at any cadence.
func (b *OpenAiBridge) AccountStatus() *auth.AccountStatus {
	b.accountMu.Lock()
	defer b.accountMu.Unlock()
	return cloneAccountStatus(b.account)
}

func cloneAccountStatus(st *auth.AccountStatus) *auth.AccountStatus {
	if st == nil {
		return nil
	}
	cp := *st
	if st.UserQuota != nil {
		q := *st.UserQuota
		cp.UserQuota = &q
	}
	if st.AddOnQuota != nil {
		q := *st.AddOnQuota
		cp.AddOnQuota = &q
	}
	if st.OrgResourcePackage != nil {
		q := *st.OrgResourcePackage
		cp.OrgResourcePackage = &q
	}
	return &cp
}

// fmtResetTime renders an epoch-millis reset instant for logs, or "unknown".
func fmtResetTime(ms int64) string {
	if ms <= 0 {
		return "unknown"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04 MST")
}

// applyJobToken builds the signed session from a jobToken response. userType
// is the tier resolved from /user/status; when empty the jobToken value is
// used, and only then does the historical default apply.
func (b *OpenAiBridge) applyJobToken(jt map[string]interface{}, userType string) {
	name, _ := jt["name"].(string)
	id, _ := jt["id"].(string)
	if userType == "" {
		userType, _ = jt["userType"].(string)
	}
	if userType == "" {
		userType = "personal_standard"
	}
	securityOauth, _ := jt["securityOauthToken"].(string)
	refreshToken, _ := jt["refreshToken"].(string)

	identity := auth.AuthIdentity{
		Name:               name,
		Aid:                id,
		UID:                id,
		UserType:           userType,
		SecurityOauthToken: securityOauth,
		RefreshToken:       refreshToken,
	}
	sess := auth.NewSession(identity, b.machineID, b.machineToken, b.machineType)

	b.mu.Lock()
	b.sess = sess
	b.identity = &identity
	b.refreshToken = refreshToken
	b.securityOauth = securityOauth
	b.expireTimeMs = toInt64(jt["expireTime"])
	b.refreshedAtMs = time.Now().UnixMilli()
	if b.expireTimeMs == 0 {
		log.Printf("[bridge] WARN gateway did not report expireTime; assuming %s validity", unknownExpiryTTL)
	}
	b.mu.Unlock()
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		val, _ := strconv.ParseInt(n, 10, 64)
		return val
	case json.Number:
		val, _ := n.Int64()
		return val
	default:
		return 0
	}
}

func (b *OpenAiBridge) needsRefresh() bool {
	nowMs := time.Now().UnixMilli()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sess == nil {
		return true
	}
	if b.expireTimeMs == 0 {
		// No expiry reported: fall back to a time-since-renewal bound so we
		// do not hit the token endpoint on every request.
		return nowMs-b.refreshedAtMs > unknownExpiryTTL.Milliseconds()
	}
	return nowMs > b.expireTimeMs-refreshMarginMs
}

func (b *OpenAiBridge) currentSess() *auth.SessionContext {
	b.mu.Lock()
	sess := b.sess
	b.mu.Unlock()
	if sess == nil {
		panic("session not bootstrapped")
	}
	return sess
}

// currentIdentity returns a snapshot of the identity taken under the lock.
// applyJobToken swaps the pointer concurrently (e.g. a 401-triggered refresh
// on another request), so an unlocked read is a data race.
func (b *OpenAiBridge) currentIdentity() *auth.AuthIdentity {
	b.mu.Lock()
	id := b.identity
	b.mu.Unlock()
	return id
}

func (b *OpenAiBridge) currentSecurityOauth() string {
	b.mu.Lock()
	token := b.securityOauth
	b.mu.Unlock()
	return token
}

// doRenew renews the session token via refreshToken.
func (b *OpenAiBridge) doRenew(ctx context.Context, force bool) error {
	b.mu.Lock()
	rt := b.refreshToken
	sot := b.securityOauth
	b.mu.Unlock()

	jt, err := auth.RefreshJobToken(ctx, b.pat, rt, sot, b.machineID, b.machineToken, b.machineType, b.Region)
	if err != nil {
		if !force {
			return err
		}
		log.Printf("[bridge] refresh rejected (%v); falling back to PAT exchange", err)
		jt, err = auth.ExchangeJobToken(ctx, b.pat, b.machineID, b.machineToken, b.machineType, b.Region)
		if err != nil {
			return err
		}
		log.Printf("[bridge] PAT re-exchange ok (exp=%v)", jt["expireTime"])
	} else {
		label := "refreshed"
		if force {
			label = "force-refreshed"
		}
		log.Printf("[bridge] session %s (exp=%v)", label, jt["expireTime"])
	}
	id, _ := jt["id"].(string)
	b.applyJobToken(jt, b.resolveAccountStatus(ctx, id))
	return nil
}

// EnsureFreshSession proactively rotates the session token before expiry.
func (b *OpenAiBridge) EnsureFreshSession(ctx context.Context) error {
	if b.bootstrapped.Load() && !b.needsRefresh() {
		return nil
	}
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	if !b.bootstrapped.Load() {
		return b.bootstrapSession(ctx)
	}
	if !b.needsRefresh() {
		return nil
	}
	return b.doRenew(ctx, false)
}

func (b *OpenAiBridge) forceRefresh(ctx context.Context) error {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	return b.doRenew(ctx, true)
}

// GetCatalog returns the dynamic model catalog (TTL cached, single-flight).
func (b *OpenAiBridge) GetCatalog(ctx context.Context) *models.ModelCatalog {
	now := float64(time.Now().Unix())
	b.catalogMu.Lock()
	cached := b.catalog
	if cached != nil && now-b.catalogTs < catalogTTL {
		b.catalogMu.Unlock()
		return cached
	}
	// Single-flight: while one goroutine refetches, everyone else (including
	// late arrivals) waits and then re-checks the cache instead of piling
	// parallel model/list calls onto the gateway.
	for b.catalogInflt {
		b.catalogMu.Unlock()
		// Cheap sleep-loop instead of sync.Cond: catalog fetches are rare
		// (once per TTL) and short (seconds).
		time.Sleep(50 * time.Millisecond)
		b.catalogMu.Lock()
		cached = b.catalog
		now = float64(time.Now().Unix())
		if cached != nil && now-b.catalogTs < catalogTTL {
			b.catalogMu.Unlock()
			return cached
		}
	}
	b.catalogInflt = true
	b.catalogMu.Unlock()
	defer func() {
		b.catalogMu.Lock()
		b.catalogInflt = false
		b.catalogMu.Unlock()
	}()

	var fetched *models.ModelCatalog
	err := b.EnsureFreshSession(ctx)
	if err == nil {
		raw, err := auth.FetchModelCatalog(ctx, b.currentSess(), b.Region)
		if err == nil {
			fetched = models.ExtractCatalog(raw)
			if fetched != nil {
				log.Printf("[models] dynamic catalog loaded: %d models [%s]", len(fetched.Keys()), strings.Join(fetched.Keys(), ", "))
			} else {
				log.Printf("[models] WARN model/list response shape unexpected; using fallback")
			}
		} else {
			log.Printf("[models] WARN dynamic fetch failed (%v); using fallback", err)
		}
	} else {
		log.Printf("[models] WARN ensure_fresh_session failed (%v); using fallback", err)
	}

	cat := fetched
	if cat == nil {
		cat = models.DefaultCatalog()
	}
	b.catalogMu.Lock()
	b.catalog = cat
	// Timestamp AFTER the fetch: stamping the pre-fetch `now` would bill the
	// fetch duration to every waiter's TTL check and keep the cache trusted
	// for longer than catalogTTL after it was actually produced.
	b.catalogTs = float64(time.Now().Unix())
	b.catalogMu.Unlock()
	return cat
}
