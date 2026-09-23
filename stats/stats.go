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
// discarding accounting that may already be correct. A boundary that still
// falls inside the cycle currently being tracked refines it without zeroing,
// so repeated polls never lose accumulated credits.
func (r *Recorder) SetBillingCycle(nextResetMs int64) {
	if nextResetMs <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.data.NextResetMs == nextResetMs {
		return
	}
	startMs := addMonthsMs(nextResetMs, -1)
	if startMs != r.data.CycleStartMs {
		// A genuinely different cycle: the allowance has been refreshed, so
		// the previous cycle's credits are stale.
		r.data.CycleCredits = 0
	}
	r.data.NextResetMs = nextResetMs
	r.data.CycleStartMs = startMs
	r.dirty = true
}

// SetAccount records the subscription metadata reported by /user/status.
//
// The panel reads this from memory, so storing it here (rather than fetching
// on demand) keeps the 15s admin poll free of upstream calls. A nil account is
// ignored so a transient status outage cannot wipe a previously known plan.
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

// addMonthsMs shifts an epoch-millis instant by n calendar months.
func addMonthsMs(ms int64, n int) int64 {
	return time.UnixMilli(ms).Local().AddDate(0, n, 0).UnixMilli()
}

// rollCycleLocked zeroes the cycle credits once the known reset boundary has
// passed, then advances the boundary locally. Caller must hold the mutex.
//
// The advance is what makes the rollover happen exactly once: without it,
// every subsequent request would still see nowMs >= NextResetMs and wipe the
// fresh total. /user/status later supplies the authoritative boundary, which
// SetBillingCycle reconciles without zeroing when it names the same cycle.
//
// A loop rather than a single step covers a service that stayed down across
// several cycles.
func (r *Recorder) rollCycleLocked(nowMs int64) {
	for r.data.NextResetMs > 0 && nowMs >= r.data.NextResetMs {
		r.data.CycleStartMs = r.data.NextResetMs
		r.data.NextResetMs = addMonthsMs(r.data.NextResetMs, 1)
		r.data.CycleCredits = 0
	}
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
	r.mu.Lock()
	defer r.mu.Unlock()

	// Roll the cycle on read as well as on write: the admin panel polls this
	// every 15s, so an idle service would otherwise keep reporting the
	// previous cycle's credits long after the boundary passed. The rollover is
	// idempotent, so this costs nothing once it has already happened.
	//
	// Dirty is set only when the boundary actually moved, so a plain read poll
	// never forces a disk write.
	prevReset := r.data.NextResetMs
	r.rollCycleLocked(time.Now().UnixMilli())
	if r.data.NextResetMs != prevReset {
		r.dirty = true
	}

	rep := &Report{
		Total:            r.data.Total,
		Success:          r.data.Success,
		Failed:           r.data.Failed,
		PromptTokens:     r.data.PromptTokens,
		CompletionTokens: r.data.CompletionTokens,
		CachedTokens:     r.data.CachedTokens,
		Credits:          r.data.Credits,
		CycleCredits:     r.data.CycleCredits,
		CycleStartMs:     r.data.CycleStartMs,
		NextResetMs:      r.data.NextResetMs,
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
