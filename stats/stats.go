// Package stats provides thread-safe request statistics aggregation
// (totals, per-model counters, hourly buckets) with periodic persistence.
package stats

import (
	"sort"
	"sync"
	"time"
)

// HourKeyFormat is the layout used for hourly bucket keys.
const HourKeyFormat = "2006-01-02T15"

// BucketRetention is how long hourly buckets are kept before pruning.
const BucketRetention = 96 * time.Hour

// cycleDriftToleranceMs is how far the reported boundary may move while still
// naming the same subscription period. The gateway recomputes the reset instant
// as "last reset + 1 month", so it drifts by hours to a couple of days; a
// genuine refresh advances the boundary by roughly a whole month. Anything in
// between is treated as the same cycle, which is what keeps an accumulated
// total from being discarded by a clock adjustment.
const cycleDriftToleranceMs = int64(7 * 24 * 60 * 60 * 1000)

// maxCycleCatchUps bounds the monthly catch-up loops: a corrupt or very old
// persisted boundary (e.g. a cold data.json from 1970) must not spend an
// unbounded number of time.Date calls under the mutex. 2400 steps is two
// centuries of monthly cycles, far more than any real deployment misses.
const maxCycleCatchUps = 2400

// maxPlausibleResetMs bounds a believable subscription boundary. It mirrors the
// guard the bridge applies when reading /user/status and the OpenAPI quota: that
// endpoint reports 9999-12-31 (253402214400000) as a "never expires" sentinel for
// plans without an expiry, and an earlier build persisted it as the cycle
// boundary.
const maxPlausibleResetMs = int64(4102444800000) // 2100-01-01T00:00:00Z

// plausibleResetMs reports whether ms can be a real subscription boundary rather
// than a missing value or a persisted sentinel.
func plausibleResetMs(ms int64) bool {
	return ms > 0 && ms < maxPlausibleResetMs
}

// ModelStat aggregates request counters for a single model.
type ModelStat struct {
	Model   string `json:"model"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// HourStat aggregates request counters for a single hour bucket.
type HourStat struct {
	Hour    string `json:"hour"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// Account is the subscription metadata reported by the gateway's
// /user/status endpoint. It is refreshed out of band (see main's account
// loop) and persisted alongside the counters so the admin panel can render it
// from memory instead of making a network call on every poll.
type Account struct {
	// Plan is the raw plan identifier ("PLAN_TIER_TEAM", ...).
	Plan string `json:"plan,omitempty"`
	// Tag is the human-facing plan label ("Teams", ...).
	Tag string `json:"tag,omitempty"`
	// OrgName identifies the organization that owns a shared resource package.
	OrgName string `json:"org_name,omitempty"`
	// IsQuotaExceeded is the OpenAPI verdict used by the official client.
	IsQuotaExceeded bool `json:"is_quota_exceeded"`
	// TotalUsagePercentage is the official aggregate ratio in [0,1].
	TotalUsagePercentage float64             `json:"total_usage_percentage"`
	UserQuota            *Quota              `json:"user_quota,omitempty"`
	AddOnQuota           *Quota              `json:"add_on_quota,omitempty"`
	OrgResourcePackage   *OrgResourcePackage `json:"org_resource_package,omitempty"`
}

// Quota is a cycle-scoped credit allowance reported by Qoder OpenAPI.
type Quota struct {
	Total      float64 `json:"total"`
	Used       float64 `json:"used"`
	Remaining  float64 `json:"remaining"`
	Percentage float64 `json:"percentage"`
	Unit       string  `json:"unit"`
	DetailURL  string  `json:"detail_url,omitempty"`
}

// OrgResourcePackage is the shared organization credit pool.
type OrgResourcePackage struct {
	Used       float64 `json:"used"`
	Cap        float64 `json:"cap"`
	Remaining  float64 `json:"remaining"`
	Percentage float64 `json:"percentage"`
	Available  bool    `json:"available"`
	Unit       string  `json:"unit"`
}

// Data is the persisted stats snapshot.
type Data struct {
	Total   int64 `json:"total"`
	Success int64 `json:"success"`
	Failed  int64 `json:"failed"`
	// Token / billing totals aggregated from the gateway usage frames.
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	Credits          float64 `json:"credits"`
	// Billing-cycle scope. The gateway refreshes the subscription allowance
	// monthly, so the lifetime Credits total above says nothing about what is
	// left this cycle. These fields make the displayed number cycle-relative.
	//
	// NextResetMs is the subscription refresh instant (epoch millis) as
	// reported by /user/status; CycleStartMs is the start of the cycle that
	// contains it; CycleCredits is credits consumed since CycleStartMs and is
	// reset automatically when the cycle rolls over. All three stay 0 when the
	// account status has never been resolved.
	NextResetMs  int64                 `json:"next_reset_ms,omitempty"`
	CycleStartMs int64                 `json:"cycle_start_ms,omitempty"`
	CycleCredits float64               `json:"cycle_credits,omitempty"`
	Account      *Account              `json:"account,omitempty"`
	ByModel      map[string]*ModelStat `json:"by_model,omitempty"`
	Hourly       map[string]*HourStat  `json:"hourly,omitempty"`
}

// ModelRow is a per-model report row served to the admin UI.
type ModelRow struct {
	Model       string  `json:"model"`
	Total       int64   `json:"total"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
}

// HourRow is a chart bucket report row served to the admin UI.
type HourRow struct {
	Hour    string `json:"hour"`
	Label   string `json:"label"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// Report is the aggregated snapshot served to the admin UI.
type Report struct {
	Total       int64   `json:"total"`
	Success     int64   `json:"success"`
	Failed      int64   `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
	// Token / billing totals.
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	Credits          float64 `json:"credits"`
	// Billing-cycle view of the credits above (all zero when the account
	// status is unknown).
	CycleCredits float64 `json:"cycle_credits"`
	CycleStartMs int64   `json:"cycle_start_ms"`
	NextResetMs  int64   `json:"next_reset_ms"`
	// Account is nil when the subscription state has never been resolved.
	Account *Account   `json:"account,omitempty"`
	ByModel []ModelRow `json:"by_model"`
	Hourly  []HourRow  `json:"hourly"`
}

// Persister persists the stats data (implemented by store.Store).
type Persister interface {
	LoadStats() *Data
	SaveStats(*Data) error
}

// Recorder aggregates request statistics with thread-safe access.
// Data is persisted lazily via Flush so hot request paths never touch disk.
type Recorder struct {
	mu      sync.Mutex
	data    *Data
	persist Persister
	dirty   bool
}

// NewRecorder creates a recorder, restoring any previously persisted data.
// The loaded data is cloned so later mutations never race with other
// store.save() calls that marshal the persisted snapshot.
func NewRecorder(p Persister) *Recorder {
	r := &Recorder{persist: p}
	if p != nil {
		if d := p.LoadStats(); d != nil {
			r.data = normalize(d).Clone()
		}
	}
	if r.data == nil {
		r.data = newData()
	}
	return r
}

func newData() *Data {
	return &Data{
		ByModel: map[string]*ModelStat{},
		Hourly:  map[string]*HourStat{},
	}
}

func normalize(d *Data) *Data {
	if d.ByModel == nil {
		d.ByModel = map[string]*ModelStat{}
	}
	if d.Hourly == nil {
		d.Hourly = map[string]*HourStat{}
	}
	return d
}

// Clone returns a deep copy of d. Callers must not hold the recorder mutex
// when the copy is needed for concurrent use.
func (d *Data) Clone() *Data {
	cp := &Data{
		Total:            d.Total,
		Success:          d.Success,
		Failed:           d.Failed,
		PromptTokens:     d.PromptTokens,
		CompletionTokens: d.CompletionTokens,
		CachedTokens:     d.CachedTokens,
		Credits:          d.Credits,
		NextResetMs:      d.NextResetMs,
		CycleStartMs:     d.CycleStartMs,
		CycleCredits:     d.CycleCredits,
		ByModel:          make(map[string]*ModelStat, len(d.ByModel)),
		Hourly:           make(map[string]*HourStat, len(d.Hourly)),
	}
	cp.Account = cloneAccount(d.Account)
	for k, v := range d.ByModel {
		cp.ByModel[k] = &ModelStat{Model: v.Model, Total: v.Total, Success: v.Success, Failed: v.Failed}
	}
	for k, v := range d.Hourly {
		cp.Hourly[k] = &HourStat{Hour: v.Hour, Total: v.Total, Success: v.Success, Failed: v.Failed}
	}
	return cp
}

// Record counts one completed request.
func (r *Recorder) Record(model string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.dirty = true
	r.data.Total++
	if ok {
		r.data.Success++
	} else {
		r.data.Failed++
	}

	m := r.data.ByModel[model]
	if m == nil {
		m = &ModelStat{Model: model}
		r.data.ByModel[model] = m
	}
	m.Total++
	if ok {
		m.Success++
	} else {
		m.Failed++
	}

	key := time.Now().Format(HourKeyFormat)
	h := r.data.Hourly[key]
	if h == nil {
		h = &HourStat{Hour: key}
		r.data.Hourly[key] = h
	}
	h.Total++
	if ok {
		h.Success++
	} else {
		h.Failed++
	}
}

// RecordUsage adds token/billing totals extracted from a gateway usage frame.
// Called once per successful request when the upstream supplies usage data;
// missing frames (error-interrupted streams) simply skip this call.
//
// Tokens are counted for every frame, but credits only for billable ones: a
// frame the gateway flags billable=false was not charged to the subscription,
// so including it would overstate consumption against the cycle allowance.
func (r *Recorder) RecordUsage(u *Usage) {
	if u == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirty = true
	r.data.PromptTokens += int64(u.PromptTokens)
	r.data.CompletionTokens += int64(u.CompletionTokens)
	r.data.CachedTokens += int64(u.CachedTokens)
	if u.NonBillable {
		return
	}
	r.data.Credits += u.Credits
	r.rollCycleLocked(time.Now().UnixMilli())
	r.data.CycleCredits += u.Credits
}

// SetBillingCycle records the subscription boundary reported by /user/status.
//
// nextResetMs is the refresh instant; the cycle start is derived by stepping
// back one calendar month, which matches the gateway's monthly refresh
// exactly (a fixed 30-day period would drift, e.g. reporting a cycle start of
// Aug 26 for a Sep 25 reset).
//
// Passing 0 (status unavailable) leaves the existing cycle intact rather than
// discarding accounting that may already be correct.
//
// The acceptance rule is forward-only, with a drift tolerance:
//
//  1. The tracked boundary is advanced locally first, so a boundary the
//     service already rolled past (the gateway's /user/status is cached for an
//     hour, so it can report a stale instant) can never move it backwards.
//  2. A boundary that does not move forward is stale: it is ignored outright,
//     keeping both the tracked cycle and its credits. This is what stops a
//     stale cached report from wiping the fresh cycle's total.
//  3. The first observation (no tracked boundary yet) adopts the boundary and
//     KEEPS the credits accumulated since boot. Credits only accumulate from
//     real requests, so they are genuinely part of this period; zeroing here
//     would discard a whole cycle's accounting on a fresh install.
//  4. A forward move within cycleDriftToleranceMs is the same subscription
//     period: the gateway recomputes the reset instant as "last reset + 1
//     month", so it drifts by hours to a couple of days. Refinement, no
//     zeroing. Comparing derived starts for (in)equality does NOT work here: a
//     month-end anchor clamps the derived start backwards enough to look like a
//     different cycle, while a two-ended drift is not contained by the old
//     interval.
//  5. A forward move of at least the tolerance is a genuine refresh, so the
//     previous cycle's credits are stale and are zeroed.
func (r *Recorder) SetBillingCycle(nextResetMs int64) {
	r.setBillingCycleAt(nextResetMs, time.Now().UnixMilli())
}

// setBillingCycleAt is SetBillingCycle with an explicit clock, so tests can use
// fixed calendar instants (month-end anchors, leap years) instead of values
// derived from the wall clock. Callers must pass the real clock otherwise.
func (r *Recorder) setBillingCycleAt(nextResetMs, nowMs int64) {
	if nextResetMs <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Advance past any boundary that has already passed: otherwise a stale
	// boundary still visible in the cached /user/status response would be
	// treated as a move backwards (or as a refresh) below. The roll is itself a
	// state change, so it must be persisted even when the reported boundary
	// then matches the rolled-forward one exactly.
	if r.rollCycleLocked(nowMs) {
		r.dirty = true
	}
	if r.data.NextResetMs == nextResetMs {
		return
	}
	if plausibleResetMs(r.data.NextResetMs) && nextResetMs <= r.data.NextResetMs {
		// Stale report: the tracked boundary is already at or beyond it. Keep
		// tracking the newer one so this cannot discard the live cycle.
		//
		// An implausible tracked boundary (a sentinel persisted by an earlier
		// build) is deliberately excluded from this comparison: it lies beyond
		// every genuine report, so treating it as "newer" would reject real
		// boundaries forever and leave the rollover dead (the credits comparison
		// below then also stays negative, so no credits are discarded either).
		return
	}
	if r.data.NextResetMs != 0 && nextResetMs-r.data.NextResetMs >= cycleDriftToleranceMs {
		// A genuinely different cycle: the allowance has been refreshed, so
		// the previous cycle's credits are stale.
		r.data.CycleCredits = 0
	}
	r.data.NextResetMs = nextResetMs
	r.data.CycleStartMs = addMonthsMs(nextResetMs, -1)
	r.dirty = true
}

// SetAccount records the subscription metadata reported by /user/status.
//
// The panel reads this from memory, so storing it here (rather than fetching
// on demand) keeps the 15s admin poll free of upstream calls. A nil account is
// ignored so a transient status outage cannot wipe a previously known plan; use
// ClearAccount to drop it on purpose.
func (r *Recorder) SetAccount(a *Account) {
	if a == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Deep-copy nested quota objects so later caller mutations cannot change
	// the persisted snapshot behind the recorder's back.
	r.data.Account = cloneAccount(a)
	r.dirty = true
}

// ClearAccount drops the cached account snapshot, e.g. after the configured PAT
// changed and the previous account's plan and allowance no longer apply.
func (r *Recorder) ClearAccount() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.data.Account == nil {
		return
	}
	r.data.Account = nil
	r.dirty = true
}

func cloneAccount(a *Account) *Account {
	if a == nil {
		return nil
	}
	cp := *a
	if a.UserQuota != nil {
		q := *a.UserQuota
		cp.UserQuota = &q
	}
	if a.AddOnQuota != nil {
		q := *a.AddOnQuota
		cp.AddOnQuota = &q
	}
	if a.OrgResourcePackage != nil {
		q := *a.OrgResourcePackage
		cp.OrgResourcePackage = &q
	}
	return &cp
}

// addMonthsMs shifts an epoch-millis instant by n calendar months, clamping the
// day to the target month's length.
//
// AddDate normalizes overflow instead of clamping (Mar 31 minus one month landed
// on Mar 3, May 31 minus one on May 1), which pushed the derived cycle start
// near the beginning of the month; since that value feeds SetBillingCycle's
// same-cycle check, every reset on a 29th-31st looked like a new cycle.
func addMonthsMs(ms int64, n int) int64 {
	return addMonthsMsIn(ms, n, time.Local)
}

// addMonthsMsIn is addMonthsMs against an explicit location. Production uses
// time.Local; tests use it to scan the historical DST-anomalous zones where a
// one-month step does not advance.
func addMonthsMsIn(ms int64, n int, loc *time.Location) int64 {
	t := time.UnixMilli(ms).In(loc)
	target := time.Date(t.Year(), t.Month()+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	last := time.Date(target.Year(), target.Month()+1, 0, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).Day()
	day := t.Day()
	if day > last {
		day = last
	}
	return time.Date(target.Year(), target.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location()).UnixMilli()
}

// nextCycleBoundary advances a reset instant by one calendar month, refusing to
// move backwards or stand still. addMonthsMs is not strictly increasing for a
// handful of historical DST-anomalous zones (America/Asuncion 1972-09,
// America/Havana, America/St_Johns), where a one-month step can consume an
// entire month's worth of offset change; without this guard the roll loops would
// never advance and would spin forever while holding the recorder lock, hanging
// every request. A non-advancing step returns 0, which callers treat as "no
// further boundary can be derived": they zero the passed cycle and clear the
// boundary so the next /user/status re-adopts it, rather than keeping a past
// boundary that would re-zero every later request's credits.
func nextCycleBoundary(nextMs int64) int64 {
	return advanceBoundaryMs(nextMs, time.Local)
}

// advanceBoundaryMs is nextCycleBoundary's decision against an explicit location.
// Production always passes time.Local; the parameter exists so the non-advancing
// guard can be tested directly, since the DST-anomalous zones that trigger it are
// historical and not the host's zone.
func advanceBoundaryMs(nextMs int64, loc *time.Location) int64 {
	adv := addMonthsMsIn(nextMs, 1, loc)
	if adv <= nextMs {
		return 0
	}
	return adv
}

// projectCycleLocked returns the cycle values that apply at nowMs without
// mutating the recorder: a read must never roll the billing cycle, otherwise an
// admin poll landing just after a boundary would persist a rollover and discard
// the previous cycle's credits.
//
// It runs the same advance loop as rollCycleLocked on local copies, so a
// projection across several missed cycles matches what the next write would
// persist. Caller must hold the mutex.
func (r *Recorder) projectCycleLocked(nowMs int64) (cycleCredits float64, startMs, nextMs int64) {
	cycleCredits = r.data.CycleCredits
	startMs = r.data.CycleStartMs
	nextMs = r.data.NextResetMs
	for i := 0; nextMs > 0 && nowMs >= nextMs && i < maxCycleCatchUps; i++ {
		startMs = nextMs
		cycleCredits = 0
		adv := nextCycleBoundary(nextMs)
		if adv == 0 {
			// No later boundary can be derived for this instant; project it as
			// unset, exactly the way rollCycleLocked drops it. Leaving the past
			// instant in place would make the projection disagree with what the
			// next write persists.
			nextMs = 0
			break
		}
		nextMs = adv
	}
	if nextMs > 0 && nowMs >= nextMs {
		// The cap was exhausted: the boundary is more than two centuries old.
		// Report it as unset rather than keep projecting a past instant forever.
		nextMs = 0
	}
	return cycleCredits, startMs, nextMs
}

// rollCycleLocked zeroes the cycle credits once the known reset boundary has
// passed, then advances the boundary locally. It reports whether any state
// changed, so the caller can mark the recorder dirty. Caller must hold the
// mutex.
//
// The advance is what makes the rollover happen exactly once: without it,
// every subsequent request would still see nowMs >= NextResetMs and wipe the
// fresh total. /user/status later supplies the authoritative boundary, which
// SetBillingCycle reconciles without zeroing when it names the same cycle.
//
// A loop rather than a single step covers a service that stayed down across
// several cycles; see nextCycleBoundary and maxCycleCatchUps for the two guards
// that keep it from spinning or doing unbounded work under the lock.
func (r *Recorder) rollCycleLocked(nowMs int64) bool {
	rolled := false
	for i := 0; r.data.NextResetMs > 0 && nowMs >= r.data.NextResetMs && i < maxCycleCatchUps; i++ {
		current := r.data.NextResetMs
		r.data.CycleStartMs = current
		r.data.CycleCredits = 0
		rolled = true
		adv := nextCycleBoundary(current)
		if adv == 0 {
			// No later boundary can be derived for this instant. Drop the
			// boundary instead of leaving it in the past, where every later
			// request would re-zero the new cycle's credits.
			r.data.NextResetMs = 0
			return true
		}
		r.data.NextResetMs = adv
	}
	if r.data.NextResetMs > 0 && nowMs >= r.data.NextResetMs {
		// The cap was exhausted: the boundary is a corrupt or centuries-old
		// value that can never be advanced into the future. Drop it so the next
		// request starts a fresh cycle instead of being zeroed on every write.
		r.data.NextResetMs = 0
		return true
	}
	return rolled
}

// Usage is the token accounting payload produced by transform.Usage.
// Declared locally to avoid an import cycle (transform is a lower layer).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	Credits          float64
	// NonBillable mirrors the gateway's charge flag, inverted on purpose: a
	// bool field named Billable would zero to false, so any caller that forgot
	// to set it would silently drop every credit. This way the zero value
	// means "billable", which is both the common case and the safe one.
	NonBillable bool
}

// Flush persists pending data if anything changed since the last flush.
// A deep copy is saved so concurrent Record calls never race with the
// persister's serialization of the snapshot.
func (r *Recorder) Flush() error {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	r.pruneLocked()
	cp := r.data.Clone()
	r.dirty = false
	r.mu.Unlock()

	if r.persist == nil {
		return nil
	}
	if err := r.persist.SaveStats(cp); err != nil {
		r.mu.Lock()
		r.dirty = true
		r.mu.Unlock()
		return err
	}
	return nil
}

// pruneLocked drops hourly buckets older than BucketRetention.
// Caller must hold the mutex.
func (r *Recorder) pruneLocked() {
	cutoff := time.Now().Add(-BucketRetention).Format(HourKeyFormat)
	for k := range r.data.Hourly {
		if k < cutoff {
			delete(r.data.Hourly, k)
		}
	}
}

// Report returns a snapshot for the admin UI: totals, per-model rows sorted by
// total (descending), and the last 24 hourly buckets in ascending order.
func (r *Recorder) Report() *Report {
	return r.reportAt(time.Now().UnixMilli())
}

// reportAt is Report with the billing-cycle projection evaluated at nowMs
// instead of the wall clock, so tests can pin fixed calendar instants (month-end
// anchors, leap years) rather than depend on the day the suite runs. Only the
// cycle view is pinned; the hourly buckets still follow the wall clock.
func (r *Recorder) reportAt(nowMs int64) *Report {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Project the cycle for the report without mutating the recorder: the
	// panel polls every 15s, so a poll landing just after a boundary would
	// otherwise roll the cycle and persist a rollover that discarded the
	// previous cycle's credits. A read is a pure projection; the write path
	// (RecordUsage) is what advances the tracked boundary.
	cycleCredits, cycleStartMs, nextResetMs := r.projectCycleLocked(nowMs)

	rep := &Report{
		Total:            r.data.Total,
		Success:          r.data.Success,
		Failed:           r.data.Failed,
		PromptTokens:     r.data.PromptTokens,
		CompletionTokens: r.data.CompletionTokens,
		CachedTokens:     r.data.CachedTokens,
		Credits:          r.data.Credits,
		CycleCredits:     cycleCredits,
		CycleStartMs:     cycleStartMs,
		NextResetMs:      nextResetMs,
		ByModel:          []ModelRow{},
		Hourly:           []HourRow{},
	}
	rep.Account = cloneAccount(r.data.Account)
	if r.data.Total > 0 {
		rep.SuccessRate = float64(r.data.Success) / float64(r.data.Total)
	}

	for _, m := range r.data.ByModel {
		rate := 0.0
		if m.Total > 0 {
			rate = float64(m.Success) / float64(m.Total)
		}
		rep.ByModel = append(rep.ByModel, ModelRow{
			Model:       m.Model,
			Total:       m.Total,
			Success:     m.Success,
			Failed:      m.Failed,
			SuccessRate: rate,
		})
	}
	sort.Slice(rep.ByModel, func(i, j int) bool {
		if rep.ByModel[i].Total != rep.ByModel[j].Total {
			return rep.ByModel[i].Total > rep.ByModel[j].Total
		}
		return rep.ByModel[i].Model < rep.ByModel[j].Model
	})

	now := time.Now()
	start := now.Add(-23 * time.Hour).Truncate(time.Hour)
	for i := 0; i < 24; i++ {
		ts := start.Add(time.Duration(i) * time.Hour)
		key := ts.Format(HourKeyFormat)
		row := HourRow{Hour: key, Label: ts.Format("15:04")}
		if h := r.data.Hourly[key]; h != nil {
			row.Total = h.Total
			row.Success = h.Success
			row.Failed = h.Failed
		}
		rep.Hourly = append(rep.Hourly, row)
	}
	return rep
}
