// Daily check-in entry point on the bridge. It reuses the per-account session,
// mints the jt- campaigns bearer, and force-refreshes once on a 401.
package bridge

import (
	"context"
	"errors"
	"fmt"

	"qoder2api/auth"
)

// Checkin performs the daily credit-claim for this account. It rotates the
// session first (a stale token surfaces as a 401 on the campaigns endpoint) and
// retries once after a force-refresh if the endpoint still rejects the token.
func (b *OpenAiBridge) Checkin(ctx context.Context) (auth.CheckinResult, error) {
	if err := b.EnsureFreshSession(ctx); err != nil {
		return auth.CheckinResult{}, fmt.Errorf("qoder check-in: session: %w", err)
	}

	// The genuine desktop fingerprint is optional: with it the six machine
	// headers are sent; without it (Linux/Docker, no runtime-info helper) none
	// are, and the daily activity is still returned. What gets the activity
	// filtered as an unofficial client is a full set of DERIVED headers.
	risk := auth.ReadRiskIdentity(ctx)

	// The campaigns endpoint rejects the COSY securityOauthToken in both
	// regions (CN: 401 TOKEN_INVALID; global: 401 TOKEN_EXPIRE) and only
	// accepts the jt- OpenAPI bearer, so check-in always exchanges — even on
	// CN, where quota/usage still takes the COSY token. A failure is surfaced
	// instead of falling back: the campaigns endpoint would reject the COSY
	// token too, so a fallback would just mask the real cause.
	bearer, err := b.openAPIBearer(ctx)
	if err != nil {
		return auth.CheckinResult{}, fmt.Errorf("qoder check-in: bearer: %w", err)
	}

	result, err := auth.CheckinWithBearer(ctx, b.currentSess(), b.pat, b.Region, bearer, risk)
	if err == nil {
		return result, nil
	}

	// A rejected token shows up as a 401 from the campaigns endpoint. Refresh
	// once and retry rather than failing the whole check-in.
	var httpErr *auth.CheckinHTTPError
	if errors.As(err, &httpErr) && httpErr.Code == 401 {
		if refreshErr := b.forceRefresh(ctx); refreshErr != nil {
			return auth.CheckinResult{}, fmt.Errorf("qoder check-in: refresh failed: %w", refreshErr)
		}
		// forceRefresh only rotates the COSY session, not the OpenAPI bearer; drop
		// the cached one so the retry actually presents a newly minted token.
		b.invalidateOpenAPIBearer()
		retryBearer, berr := b.openAPIBearer(ctx)
		if berr != nil {
			return auth.CheckinResult{}, fmt.Errorf("qoder check-in: bearer: %w", berr)
		}
		return auth.CheckinWithBearer(ctx, b.currentSess(), b.pat, b.Region, retryBearer, risk)
	}
	return auth.CheckinResult{}, err
}
