// Daily check-in entry point on the bridge. It reuses the per-account session,
// picks the region's campaign bearer, and force-refreshes once on a 401.
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

	// The campaign service only returns the claimable daily-credit activity to
	// the genuine desktop fingerprint; without it the activity is dropped and we
	// would wrongly report "no campaign". Read it from the installed app helper.
	risk := auth.ReadRiskIdentity(ctx)

	// CN accepts the COSY securityOauthToken on the campaign endpoint; global
	// rejects it (401 TOKEN_EXPIRE) and requires the OpenAPI bearer. quotaBearer
	// returns the right one per region (CN: securityOauth; global: OpenAPI token).
	// A failure is surfaced instead of falling back to the COSY token: global
	// only rejects that one, so the fallback would just mask the real cause.
	bearer, err := b.quotaBearer(ctx)
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
		retryBearer, berr := b.quotaBearer(ctx)
		if berr != nil {
			return auth.CheckinResult{}, fmt.Errorf("qoder check-in: bearer: %w", berr)
		}
		return auth.CheckinWithBearer(ctx, b.currentSess(), b.pat, b.Region, retryBearer, risk)
	}
	return auth.CheckinResult{}, err
}
