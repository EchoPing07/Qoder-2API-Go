// Package bridge implements the OpenAI-compatible API bridge with session
// management and streaming/sync forwarding for a single Qoder PAT.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"qoder2api/auth"
	"qoder2api/models"

	"github.com/google/uuid"
)

// chatMaxBodyBytes caps the /v1/chat/completions request body to bound memory
// use against oversized payloads (DoS hardening).
const chatMaxBodyBytes = 10 << 20 // 10 MiB

// marshalNoEscape serializes v to compact JSON without HTML escaping.
func marshalNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// --- OpenAiBridge ---

// OpenAiBridge manages a single PAT's session and forwards chat requests.
type OpenAiBridge struct {
	Region *auth.RegionConfig
	pat    string

	machineID    string
	machineToken string
	machineType  string

	mu            sync.Mutex // guards sess/identity/tokens/expiry/template
	refreshMu     sync.Mutex // serializes renewal network calls
	sess          *auth.SessionContext
	identity      *auth.AuthIdentity
	refreshToken  string
	securityOauth string
	expireTimeMs  int64
	refreshedAtMs int64
	bootstrapped  atomic.Bool

	catalogMu    sync.Mutex
	catalog      *models.ModelCatalog
	catalogTs    float64
	catalogInflt bool // single-flight: a fetch is already in progress

	// account caches the /user/status result: the real subscription tier
	// (which jobToken does not report) and the billing-cycle boundary.
	accountMu sync.Mutex
	account   *auth.AccountStatus
	accountTs float64

	// chatSlots bounds concurrent upstream chat requests per PAT. Exceeding
	// the gateway's per-account admission window yields business code 10605
	// ("gateway busy") over HTTP 401/403, so we queue locally instead.
	chatSlots chan struct{}
}

// NewOpenAiBridge creates a new bridge for the given PAT.
func NewOpenAiBridge(pat string, region *auth.RegionConfig) *OpenAiBridge {
	if region == nil {
		region = auth.CN
	}
	machineID := uuid.New().String()
	rawToken := (uuid.New().String() + uuid.New().String())
	if len(rawToken) > 50 {
		rawToken = rawToken[:50]
	}
	machineToken := rawToken
	machineType := uuid.New().String()
	if len(machineType) > 18 {
		machineType = machineType[:18]
	}
	return &OpenAiBridge{
		Region:       region,
		pat:          pat,
		machineID:    machineID,
		machineToken: machineToken,
		machineType:  machineType,
		chatSlots:    make(chan struct{}, maxUpstreamConcurrency()),
	}
}

// maxUpstreamConcurrency returns the per-PAT limit on concurrent upstream
// chat requests. The Qoder gateway enforces a strict per-account admission
// window per model and rejects excess requests with business code 10605
// (delivered over HTTP 401/403), so requests are queued locally by default
// instead of tripping the gateway limiter. Override via QODER_MAX_CONCURRENCY.
func maxUpstreamConcurrency() int {
	if v := os.Getenv("QODER_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 32 {
			return n
		}
		log.Printf("[bridge] WARN invalid QODER_MAX_CONCURRENCY=%q; using 1", v)
	}
	return 1
}

// busyFallbackBackoff is used when a 10605 rejection carries no usable
// retryAfterSeconds (or an implausible one). A package var so tests can
// shorten the wait.
var busyFallbackBackoff = 2 * time.Second

// openStreamAsync opens the SSE stream with auth retry (refresh once on 401
// before any content is produced) and busy back-off (gateway queue/
// concurrency rejections, code 10605, are retried once after the server-
// suggested delay instead of triggering a pointless token refresh).
func (b *OpenAiBridge) openStreamAsync(ctx context.Context, url string, jsonBody []byte, extraHeaders map[string]string, callback func(line string) error) error {
	for attempt := 1; ; attempt++ {
		sess := b.currentSess()
		produced := false
		err := auth.OpenStreamLines(ctx, sess, url, jsonBody, extraHeaders, func(line string) error {
			produced = true
			return callback(line)
		})
		if err == nil {
			return nil
		}
		var authErr *auth.AuthError
		if errors.As(err, &authErr) {
			if produced || attempt >= 2 {
				return err
			}
			log.Printf("[bridge] auth error before any content (%v); refreshing and retrying once", err)
			if refreshErr := b.forceRefresh(ctx); refreshErr != nil {
				log.Printf("[bridge] reactive refresh failed: %v", refreshErr)
				return err
			}
			continue
		}
		var busyErr *auth.BusyError
		if errors.As(err, &busyErr) {
			if produced || attempt >= 2 {
				return err
			}
			wait := busyErr.RetryAfter
			if wait <= 0 || wait > 10*time.Second {
				wait = busyFallbackBackoff
			}
			log.Printf("[bridge] gateway busy (%s); backing off %s then retrying once", busyErr.Code, wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		return err
	}
}

func sortStrings(s []string) {
	sort.Strings(s)
}

// truncateRunes caps s to at most n runes, never splitting a multi-byte
// UTF-8 sequence (a byte slice like prompt[:30] can produce invalid UTF-8
// for CJK input, which the gateway sees as U+FFFD replacement characters).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
