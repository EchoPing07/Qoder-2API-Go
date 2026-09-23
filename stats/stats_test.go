package stats

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRecordAndReport(t *testing.T) {
	r := NewRecorder(nil)
	r.Record("Qwen3.7-Max", true)
	r.Record("Qwen3.7-Max", true)
	r.Record("Qwen3.7-Max", false)
	r.Record("DeepSeek-V4-Pro", true)

	rep := r.Report()
	if rep.Total != 4 {
		t.Errorf("expected total 4, got %d", rep.Total)
	}
	if rep.Success != 3 {
		t.Errorf("expected success 3, got %d", rep.Success)
	}
	if rep.Failed != 1 {
		t.Errorf("expected failed 1, got %d", rep.Failed)
	}
	if rep.SuccessRate != 0.75 {
		t.Errorf("expected rate 0.75, got %f", rep.SuccessRate)
	}
	if len(rep.ByModel) != 2 {
		t.Fatalf("expected 2 model rows, got %d", len(rep.ByModel))
	}
	// Sorted by total descending: Qwen3.7-Max first
	if rep.ByModel[0].Model != "Qwen3.7-Max" || rep.ByModel[0].Total != 3 {
		t.Errorf("expected Qwen3.7-Max first with total 3, got %+v", rep.ByModel[0])
	}
	if rep.ByModel[1].Model != "DeepSeek-V4-Pro" || rep.ByModel[1].Failed != 0 {
		t.Errorf("unexpected second row: %+v", rep.ByModel[1])
	}
	if len(rep.Hourly) != 24 {
		t.Errorf("expected 24 hourly buckets, got %d", len(rep.Hourly))
	}
	totalInHourly := int64(0)
	for _, h := range rep.Hourly {
		totalInHourly += h.Total
	}
	if totalInHourly != 4 {
		t.Errorf("expected 4 total across hourly buckets, got %d", totalInHourly)
	}
}

func TestFlushPersists(t *testing.T) {
	var saved *Data
	p := &fakePersister{
		saveFn: func(d *Data) error {
			cp := *d
			saved = &cp
			return nil
		},
	}
	r := NewRecorder(p)
	r.Record("GLM-5.2", false)
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if saved == nil || saved.Total != 1 || saved.Failed != 1 {
		t.Fatalf("expected persisted data total=1 failed=1, got %+v", saved)
	}
	if p.saveCalls != 1 {
		t.Errorf("expected exactly 1 save call, got %d", p.saveCalls)
	}
	// A second flush with no new records must not persist again.
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if p.saveCalls != 1 {
		t.Errorf("expected no additional save call, got %d", p.saveCalls)
	}
}

func TestRestoreFromPersister(t *testing.T) {
	p := &fakePersister{loadFn: func() *Data {
		d := newData()
		d.Total = 7
		d.Success = 6
		d.Failed = 1
		d.ByModel["Qwen3.6-Flash"] = &ModelStat{Model: "Qwen3.6-Flash", Total: 7, Success: 6, Failed: 1}
		return d
	}}
	r := NewRecorder(p)
	rep := r.Report()
	if rep.Total != 7 || rep.Success != 6 || rep.Failed != 1 {
		t.Errorf("expected restored 7/6/1, got %d/%d/%d", rep.Total, rep.Success, rep.Failed)
	}
	if len(rep.ByModel) != 1 {
		t.Errorf("expected 1 restored model row, got %d", len(rep.ByModel))
	}
}

func TestPruneRemovesOldBuckets(t *testing.T) {
	r := NewRecorder(nil)
	r.data.Hourly["2020-01-01T00"] = &HourStat{Hour: "2020-01-01T00", Total: 1}
	r.data.Hourly[time.Now().Format(HourKeyFormat)] = &HourStat{Hour: "now", Total: 2}
	r.Record("Kimi-K2.7-Code", true)
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	if _, ok := r.data.Hourly["2020-01-01T00"]; ok {
		t.Error("expected ancient bucket to be pruned")
	}
	total := int64(0)
	for _, h := range r.data.Hourly {
		total += h.Total
	}
	if total != 3 {
		t.Errorf("expected 3 remaining records, got %d", total)
	}
}

func TestNilPersisterSafe(t *testing.T) {
	r := NewRecorder(nil)
	r.Record("x", true)
	if err := r.Flush(); err != nil {
		t.Errorf("Flush with nil persister should not fail, got %v", err)
	}
}

func TestRecordUsageAggregates(t *testing.T) {
	r := NewRecorder(nil)
	r.RecordUsage(&Usage{PromptTokens: 17, CompletionTokens: 70, CachedTokens: 3, Credits: 0.005279472})
	r.RecordUsage(&Usage{PromptTokens: 14, CompletionTokens: 341, CachedTokens: 0, Credits: 0.02449533})
	r.RecordUsage(nil) // no-op guard

	rep := r.Report()
	if rep.PromptTokens != 31 {
		t.Errorf("expected 31 prompt tokens, got %d", rep.PromptTokens)
	}
	if rep.CompletionTokens != 411 {
		t.Errorf("expected 411 completion tokens, got %d", rep.CompletionTokens)
	}
	if rep.CachedTokens != 3 {
		t.Errorf("expected 3 cached tokens, got %d", rep.CachedTokens)
	}
	want := 0.005279472 + 0.02449533
	if rep.Credits < want-1e-9 || rep.Credits > want+1e-9 {
		t.Errorf("expected credits %v, got %v", want, rep.Credits)
	}

	// Usage must persist through Flush like the counters do, and restored
	// data carries prior usage forward.
	r2 := NewRecorder(nil)
	r2.data.PromptTokens = 5 // restored data carries prior usage
	r2.RecordUsage(&Usage{PromptTokens: 1, CompletionTokens: 2, CachedTokens: 0, Credits: 0.5})
	if r2.Report().PromptTokens != 6 {
		t.Errorf("expected restored+new prompt tokens 6, got %d", r2.Report().PromptTokens)
	}
}

// The persisted snapshot must be a deep copy: later Record calls must never
// mutate what was already handed to the persister.
func TestFlushPersistsSnapshotCopy(t *testing.T) {
	var saved *Data
	p := &fakePersister{
		saveFn: func(d *Data) error {
			saved = d
			return nil
		},
	}
	r := NewRecorder(p)
	r.Record("A", true)
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	r.Record("A", true)
	r.Record("B", false)
	if saved.Total != 1 {
		t.Errorf("saved snapshot mutated by later records: total=%d", saved.Total)
	}
	if _, ok := saved.ByModel["B"]; ok {
		t.Error("saved snapshot mutated by later records: model B leaked in")
	}
	if saved.ByModel["A"] == nil || saved.ByModel["A"].Total != 1 {
		t.Errorf("saved per-model stat mutated: %+v", saved.ByModel["A"])
	}
}

// Restored data must be cloned: recording must not mutate the persister's
// original object (which may be aliased by store.config.Stats).
func TestRestoreIsCloned(t *testing.T) {
	orig := newData()
	orig.Total = 3
	orig.ByModel["M"] = &ModelStat{Model: "M", Total: 3}
	p := &fakePersister{loadFn: func() *Data { return orig }}
	r := NewRecorder(p)
	r.Record("M", true)
	if orig.Total != 3 {
		t.Errorf("recorder mutated persisted data: total=%d", orig.Total)
	}
	if orig.ByModel["M"].Total != 3 {
		t.Errorf("recorder mutated persisted per-model stat: %+v", orig.ByModel["M"])
	}
}

// Concurrent Record/Flush/Report must be race-free (run with -race).
func TestConcurrentRecordFlushReport(t *testing.T) {
	r := NewRecorder(&fakePersister{})
	const workers = 8
	const perWorker = 400
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				r.Record("Model-"+strconv.Itoa(g), i%3 != 0)
			}
		}(g)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				r.Flush()
			}
		}
	}()
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = r.Report()
			}
		}()
	}
	wg.Wait()
	close(done)
	rep := r.Report()
	if rep.Total != workers*perWorker {
		t.Errorf("expected total %d, got %d", workers*perWorker, rep.Total)
	}
	if len(rep.ByModel) != workers {
		t.Errorf("expected %d model rows, got %d", workers, len(rep.ByModel))
	}
}

type fakePersister struct {
	loadFn    func() *Data
	saveFn    func(*Data) error
	saveCalls int
}

func (f *fakePersister) LoadStats() *Data {
	if f.loadFn == nil {
		return nil
	}
	return f.loadFn()
}

func (f *fakePersister) SaveStats(d *Data) error {
	f.saveCalls++
	if f.saveFn == nil {
		return nil
	}
	return f.saveFn(d)
}

// NonBillable frames must contribute tokens (the work really happened) but not
// credits (nothing was charged to the subscription allowance).
func TestRecordUsageNonBillableSkipsCreditsOnly(t *testing.T) {
	r := NewRecorder(nil)
	r.RecordUsage(&Usage{PromptTokens: 10, CompletionTokens: 20, Credits: 0.5, NonBillable: true})
	r.RecordUsage(&Usage{PromptTokens: 1, CompletionTokens: 2, Credits: 0.25})

	rep := r.Report()
	if rep.PromptTokens != 11 || rep.CompletionTokens != 22 {
		t.Errorf("tokens must count non-billable frames too: got prompt=%d completion=%d, want 11/22",
			rep.PromptTokens, rep.CompletionTokens)
	}
	if rep.Credits < 0.25-1e-9 || rep.Credits > 0.25+1e-9 {
		t.Errorf("expected only the billable credits 0.25, got %v", rep.Credits)
	}
	if rep.CycleCredits < 0.25-1e-9 || rep.CycleCredits > 0.25+1e-9 {
		t.Errorf("expected cycle credits 0.25, got %v", rep.CycleCredits)
	}
}

// The zero value of Usage must mean billable: a caller that forgets the flag
// should over-count credits rather than silently lose all of them.
func TestRecordUsageDefaultsToBillable(t *testing.T) {
	r := NewRecorder(nil)
	r.RecordUsage(&Usage{Credits: 1.5})
	if got := r.Report().CycleCredits; got < 1.5-1e-9 || got > 1.5+1e-9 {
		t.Errorf("expected 1.5 credits with the flag unset, got %v", got)
	}
}

// SetBillingCycle derives the cycle start by stepping back one calendar month,
// which must land on the gateway's own boundary rather than a fixed 30 days.
// For the observed Sep 25 reset that is Aug 25, not Aug 26.
func TestSetBillingCycleDerivesCalendarStart(t *testing.T) {
	r := NewRecorder(nil)
	// Kept in the future so the report's cycle projection never advances it.
	year := time.Now().Year() + 4
	reset := time.Date(year, time.September, 25, 0, 0, 0, 0, time.Local)
	r.SetBillingCycle(reset.UnixMilli())

	rep := r.Report()
	if rep.NextResetMs != reset.UnixMilli() {
		t.Fatalf("expected next reset %d, got %d", reset.UnixMilli(), rep.NextResetMs)
	}
	wantStart := time.Date(year, time.August, 25, 0, 0, 0, 0, time.Local)
	if rep.CycleStartMs != wantStart.UnixMilli() {
		t.Errorf("expected cycle start %s, got %s",
			wantStart.Format(time.RFC3339), time.UnixMilli(rep.CycleStartMs).Format(time.RFC3339))
	}
}

// addMonthsMs must clamp the day to the target month's length instead of
// normalizing the overflow (AddDate turned Mar 31 minus one month into Mar 3);
// a derived cycle start near the beginning of the month made every 29th-31st
// reset look like a new cycle.
func TestAddMonthsMsClampsMonthEnd(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		n    int
		want time.Time
	}{
		{"mar-31-minus-1", time.Date(2026, time.March, 31, 12, 30, 45, 0, time.Local), -1,
			time.Date(2026, time.February, 28, 12, 30, 45, 0, time.Local)},
		{"mar-30-minus-1", time.Date(2026, time.March, 30, 0, 0, 0, 0, time.Local), -1,
			time.Date(2026, time.February, 28, 0, 0, 0, 0, time.Local)},
		{"mar-29-minus-1", time.Date(2026, time.March, 29, 0, 0, 0, 0, time.Local), -1,
			time.Date(2026, time.February, 28, 0, 0, 0, 0, time.Local)},
		{"may-31-minus-1", time.Date(2026, time.May, 31, 8, 0, 0, 0, time.Local), -1,
			time.Date(2026, time.April, 30, 8, 0, 0, 0, time.Local)},
		{"jan-31-plus-1", time.Date(2026, time.January, 31, 8, 0, 0, 0, time.Local), 1,
			time.Date(2026, time.February, 28, 8, 0, 0, 0, time.Local)},
		{"aug-31-plus-1", time.Date(2026, time.August, 31, 8, 0, 0, 0, time.Local), 1,
			time.Date(2026, time.September, 30, 8, 0, 0, 0, time.Local)},
		{"leap-mar-31-minus-1", time.Date(2028, time.March, 31, 8, 0, 0, 0, time.Local), -1,
			time.Date(2028, time.February, 29, 8, 0, 0, 0, time.Local)},
		// A non-month-end instant keeps its day-of-month and time-of-day.
		{"mid-month-plus-1", time.Date(2026, time.February, 15, 23, 59, 59, 123456789, time.Local), 1,
			time.Date(2026, time.March, 15, 23, 59, 59, 123456789, time.Local)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := addMonthsMs(tc.in.UnixMilli(), tc.n)
			if got != tc.want.UnixMilli() {
				t.Errorf("addMonthsMs(%s, %d) = %s, want %s",
					tc.in.Format(time.RFC3339), tc.n,
					time.UnixMilli(got).Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// Repeatedly polling the same boundary must not discard credits already
// accumulated inside the live cycle.
func TestSetBillingCycleIdempotent(t *testing.T) {
	r := NewRecorder(nil)
	reset := time.Now().Add(72 * time.Hour).UnixMilli()
	r.SetBillingCycle(reset)
	r.RecordUsage(&Usage{Credits: 2})
	r.SetBillingCycle(reset) // a redundant refresh from the account loop

	if got := r.Report().CycleCredits; got < 2-1e-9 || got > 2+1e-9 {
		t.Errorf("expected cycle credits to survive a redundant boundary set, got %v", got)
	}
}

// A boundary drifting inside the live cycle (the gateway recomputes the reset
// instant from "last reset + 1 month") still describes the same subscription
// period and must keep the accumulated credits.
//
// The instants are fixed (with an explicit clock) so this cannot become
// calendar-dependent: a wall-clock anchor near a month end clamps the derived
// start and would double-count the month-end case below.
func TestSetBillingCycleKeepsCreditsOnBoundaryDrift(t *testing.T) {
	base := time.Date(2026, time.September, 25, 8, 0, 0, 0, time.Local)
	now := base.Add(-72 * time.Hour).UnixMilli()
	for _, drift := range []time.Duration{time.Hour, 6 * time.Hour, 48 * time.Hour} {
		t.Run(drift.String(), func(t *testing.T) {
			r := NewRecorder(nil)
			r.setBillingCycleAt(base.UnixMilli(), now)
			oldStart := r.data.CycleStartMs
			// Seed directly and pin the report clock: RecordUsage and Report read
			// the wall clock, which would roll this fixed instant once the suite
			// runs past Sep 25 2026.
			r.data.CycleCredits = 3

			drifted := base.Add(drift)
			r.setBillingCycleAt(drifted.UnixMilli(), now)

			rep := r.reportAt(now)
			// The drift moves both end points, so the new interval is neither
			// equal nor contained in the old one, yet it names the same period.
			if r.data.CycleStartMs == oldStart {
				t.Fatal("expected the stored cycle start to drift as well")
			}
			if rep.CycleCredits < 3-1e-9 || rep.CycleCredits > 3+1e-9 {
				t.Errorf("expected a same-cycle drift to keep 3 credits, got %v", rep.CycleCredits)
			}
			if rep.NextResetMs != drifted.UnixMilli() || rep.CycleStartMs != addMonthsMs(drifted.UnixMilli(), -1) {
				t.Errorf("expected the drifted boundaries to be stored, got start=%d next=%d",
					rep.CycleStartMs, rep.NextResetMs)
			}
		})
	}
}

// A genuine refresh advances the boundary by about a month and must zero the
// previous cycle's credits, including for the month-end anchors whose derived
// start clamps backwards (Jul 31 -> Aug 31 -> Sep 30 derived Aug 30, which sits
// BEFORE the tracked boundary and used to make the overlap test keep stale
// credits).
func TestSetBillingCycleNextCycleZeroesCredits(t *testing.T) {
	cases := []struct {
		name      string
		stored    time.Time
		reported  time.Time
		wantStart time.Time
	}{
		{"month-end-31st", time.Date(2026, time.August, 31, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.September, 30, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.August, 30, 8, 0, 0, 0, time.Local)},
		{"month-end-30th", time.Date(2026, time.August, 30, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.September, 30, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.August, 30, 8, 0, 0, 0, time.Local)},
		// 29th/30th anchors whose derived start clamps to Jan 28, i.e. BEFORE the
		// stored Jan 29/30 boundary: exactly the B2 shape the overlap test missed.
		{"month-end-29th-clamped", time.Date(2026, time.January, 29, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.February, 28, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.January, 28, 8, 0, 0, 0, time.Local)},
		{"month-end-30th-clamped", time.Date(2026, time.January, 30, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.February, 28, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.January, 28, 8, 0, 0, 0, time.Local)},
		{"leap-year", time.Date(2028, time.February, 29, 8, 0, 0, 0, time.Local),
			time.Date(2028, time.March, 29, 8, 0, 0, 0, time.Local),
			time.Date(2028, time.February, 29, 8, 0, 0, 0, time.Local)},
		{"mid-month", time.Date(2026, time.September, 25, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.October, 25, 8, 0, 0, 0, time.Local),
			time.Date(2026, time.September, 25, 8, 0, 0, 0, time.Local)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRecorder(nil)
			// The clock is pinned before the tracked boundary so adopting it is a
			// pure first observation; the credits are seeded directly for the
			// same reason (RecordUsage reads the wall clock).
			now := tc.stored.Add(-72 * time.Hour).UnixMilli()
			r.setBillingCycleAt(tc.stored.UnixMilli(), now)
			r.data.CycleCredits = 42

			r.setBillingCycleAt(tc.reported.UnixMilli(), now)

			// Pin the report clock: the stored boundary sits in the past half of
			// the table, which a bare Report would roll once real time passes it.
			rep := r.reportAt(now)
			if rep.CycleCredits != 0 {
				t.Errorf("expected a genuine refresh to zero the old credits, got %v", rep.CycleCredits)
			}
			if rep.NextResetMs != tc.reported.UnixMilli() {
				t.Errorf("expected the new boundary %d, got %d", tc.reported.UnixMilli(), rep.NextResetMs)
			}
			if rep.CycleStartMs != tc.wantStart.UnixMilli() {
				t.Errorf("expected cycle start %s, got %s",
					tc.wantStart.Format(time.RFC3339), time.UnixMilli(rep.CycleStartMs).Format(time.RFC3339))
			}
		})
	}
}

// Credits accumulated before the first successful /user/status sync (no tracked
// boundary yet) are genuinely part of the period: adopting the boundary must not
// discard them, otherwise a fresh install reports 0 for everything billed since
// boot.
func TestSetBillingCycleKeepsPreSyncCredits(t *testing.T) {
	r := NewRecorder(nil)
	r.RecordUsage(&Usage{Credits: 2})
	r.RecordUsage(&Usage{Credits: 2})
	if got := r.Report().CycleCredits; got != 4 {
		t.Fatalf("expected the pre-sync credits to accumulate, got %v", got)
	}

	reset := time.Date(2026, time.October, 25, 8, 0, 0, 0, time.Local)
	now := reset.Add(-72 * time.Hour).UnixMilli()
	r.setBillingCycleAt(reset.UnixMilli(), now)

	rep := r.reportAt(now)
	if rep.CycleCredits != 4 {
		t.Errorf("the first boundary observation discarded pre-sync credits: %v", rep.CycleCredits)
	}
	if rep.NextResetMs != reset.UnixMilli() {
		t.Errorf("expected the boundary to be adopted, got %d", rep.NextResetMs)
	}
	if rep.CycleStartMs != addMonthsMs(reset.UnixMilli(), -1) {
		t.Errorf("expected the derived cycle start, got %d", rep.CycleStartMs)
	}
}

// /user/status is cached for an hour, so a report can name a boundary the service
// already rolled past. That stale report must not move the tracked boundary
// backwards nor wipe the fresh cycle's credits.
func TestSetBillingCycleIgnoresStaleBoundary(t *testing.T) {
	r := NewRecorder(nil)
	stale := time.Now().Add(-time.Hour).UnixMilli()
	r.setBillingCycleAt(stale, stale)
	// A request after the boundary rolls into the new cycle and bills against it.
	r.RecordUsage(&Usage{Credits: 2})
	if got := r.Report().CycleCredits; got != 2 {
		t.Fatalf("expected 2 credits in the rolled cycle, got %v", got)
	}

	// The cached status still reports the boundary that already passed.
	r.dirty = false
	r.SetBillingCycle(stale)
	if r.dirty {
		t.Error("a stale boundary must be ignored without marking the data dirty")
	}

	rep := r.Report()
	if rep.CycleCredits != 2 {
		t.Errorf("a stale cached boundary wiped the new cycle's credits: %v", rep.CycleCredits)
	}
	if rep.NextResetMs <= time.Now().UnixMilli() {
		t.Errorf("a stale cached boundary moved the tracked boundary backwards: %d", rep.NextResetMs)
	}
}

// TestSetBillingCycleRecoversFromPersistedSentinel covers the upgrade path: an
// earlier build adopted the OpenAPI "never expires" sentinel (9999-12-31) as the
// cycle boundary. The fixed bridge stops reporting it, but the sentinel is still
// in data.json, and it lies beyond every genuine boundary — so the forward-only
// rule would reject all real reports and leave the rollover permanently dead.
// Adopting the real boundary must restore both the boundary and the rollover,
// while keeping the credits (the sentinel cycle never really ended, so the
// accumulated total still belongs to the period now being adopted).
func TestSetBillingCycleRecoversFromPersistedSentinel(t *testing.T) {
	const sentinel = int64(253402214400000) // 9999-12-31, written by the old build
	r := NewRecorder(nil)
	r.setBillingCycleAt(sentinel, 1000)
	r.RecordUsage(&Usage{Credits: 10})
	if got := r.Report().CycleCredits; got != 10 {
		t.Fatalf("expected the sentinel cycle to accumulate, got %v", got)
	}

	real := time.Date(2026, time.July, 16, 17, 27, 0, 0, time.Local)
	now := real.Add(-24 * time.Hour).UnixMilli()
	r.setBillingCycleAt(real.UnixMilli(), now)

	rep := r.reportAt(now)
	if rep.NextResetMs != real.UnixMilli() {
		t.Errorf("the sentinel still masks the real boundary: got %d, want %d",
			rep.NextResetMs, real.UnixMilli())
	}
	if rep.CycleStartMs != addMonthsMs(real.UnixMilli(), -1) {
		t.Errorf("expected the derived cycle start %d, got %d", addMonthsMs(real.UnixMilli(), -1), rep.CycleStartMs)
	}
	if rep.CycleCredits != 10 {
		t.Errorf("recovering from the sentinel discarded live credits: %v", rep.CycleCredits)
	}

	// The rollover must work again: a request past the real boundary moves the
	// tracked boundary forward instead of being zeroed forever.
	r.RecordUsage(&Usage{Credits: 1})
	after := r.reportAt(real.Add(time.Hour).UnixMilli())
	if after.CycleCredits != 1 {
		t.Errorf("expected the rollover to resume after recovery, got %v", after.CycleCredits)
	}
	if after.NextResetMs <= real.UnixMilli() {
		t.Errorf("expected the boundary to advance past the adopted one, got %d", after.NextResetMs)
	}
}

// Report projects the rolled view while the stored state still holds the old
// cycle. A later boundary refinement must not resurrect credits the panel has
// already reported as zero.
func TestReportAndSetBillingCycleDoNotResurrectCredits(t *testing.T) {
	r := NewRecorder(nil)
	boundary := time.Now().Add(-time.Hour).UnixMilli()
	r.setBillingCycleAt(boundary, boundary)
	r.data.CycleCredits = 5

	if got := r.Report().CycleCredits; got != 0 {
		t.Fatalf("expected the passed boundary to project 0 credits, got %v", got)
	}

	// A refinement that still names the same (future) period must not bring the
	// already-projected credits back.
	r.SetBillingCycle(addMonthsMs(boundary, 1) + int64(30*time.Minute/time.Millisecond))
	if got := r.Report().CycleCredits; got != 0 {
		t.Errorf("a refinement resurrected credits already reported as 0: %v", got)
	}
}

// A non-advancing monthly step (a few historical DST-anomalous zones consume an
// entire month's offset change in one step) must not be treated as a valid
// advance: the roll loops would spin forever holding the recorder lock, hanging
// every request. nextCycleBoundary refuses it, and the loops stop.
func TestNextCycleBoundaryRejectsNonAdvancingStep(t *testing.T) {
	loc, bad := nonAdvancingInstant(t)
	if adv := advanceBoundaryMs(bad, loc); adv != 0 {
		t.Errorf("expected a non-advancing step to be rejected, got %d", adv)
	}

	// An ordinary instant in the same zone still advances, so the guard does
	// not disable the roll.
	normal := time.Date(2026, time.June, 15, 12, 0, 0, 0, loc).UnixMilli()
	want := addMonthsMsIn(normal, 1, loc)
	if want <= normal {
		t.Fatalf("test setup: %s does not advance normally", time.UnixMilli(normal).In(loc))
	}
	if adv := advanceBoundaryMs(normal, loc); adv != want {
		t.Errorf("expected an ordinary monthly step to advance to %d, got %d", want, adv)
	}
}

// The roll loops must terminate even for a boundary that can never be advanced
// into the future (the historical anomaly above, or a corrupt persisted value).
// Holding the recorder lock forever would hang every chat request.
func TestCycleRollTerminatesOnUnadvanceableBoundary(t *testing.T) {
	loc, bad := nonAdvancingInstant(t)

	t.Run("dst-anomaly", func(t *testing.T) {
		// Production helpers always step time.Local, so point it at the anomalous
		// zone for this subtest. Tests in a package run sequentially, and the
		// subtest restores the zone before the next one starts.
		restore := time.Local
		time.Local = loc
		defer func() { time.Local = restore }()

		r := NewRecorder(nil)
		r.data.NextResetMs = bad
		r.data.CycleStartMs = addMonthsMsIn(bad, -1, loc)
		r.data.CycleCredits = 9

		// The read path must terminate and drop the undecodable boundary instead
		// of keeping a past instant that re-zeros every later write.
		rep := reportWithTimeout(t, r)
		if rep.CycleCredits != 0 {
			t.Errorf("expected the passed cycle to project 0 credits, got %v", rep.CycleCredits)
		}
		if rep.NextResetMs != 0 {
			t.Errorf("expected the undecodable boundary to project as unset, got %d", rep.NextResetMs)
		}

		// The write path must terminate too, and the credits of the new cycle
		// must survive instead of being wiped by the stale boundary on every
		// request.
		r.RecordUsage(&Usage{Credits: 3})
		if r.data.NextResetMs != 0 {
			t.Errorf("expected the write path to drop the boundary, got %d", r.data.NextResetMs)
		}
		if got := r.Report().CycleCredits; got != 3 {
			t.Errorf("expected the new cycle to accumulate 3 credits, got %v", got)
		}

		// Recovery: once the boundary is dropped, the next /user/status report is
		// a first observation again. It must be adopted and must KEEP the credits
		// accumulated while no boundary was known, otherwise every recovery would
		// silently reset the panel to 0.
		fresh := time.Now().Add(72 * time.Hour).UnixMilli()
		r.setBillingCycleAt(fresh, time.Now().UnixMilli())
		if r.data.NextResetMs != fresh {
			t.Errorf("expected the dropped boundary to be re-adopted, got %d", r.data.NextResetMs)
		}
		if got := r.Report().CycleCredits; got != 3 {
			t.Errorf("re-adopting the boundary discarded the credits: got %v, want 3", got)
		}
	})

	t.Run("corrupt-far-past", func(t *testing.T) {
		// A corrupt boundary far in the past exercises the catch-up cap instead.
		r := NewRecorder(nil)
		r.data.NextResetMs = int64(1) // 1970
		r.data.CycleCredits = 5
		if rep := reportWithTimeout(t, r); rep.NextResetMs <= time.Now().UnixMilli() {
			t.Errorf("expected the catch-up to reach the present, got %d", rep.NextResetMs)
		}
	})
}

// reportWithTimeout calls Report in a goroutine so a spinning loop fails the
// test instead of hanging the whole run.
func reportWithTimeout(t *testing.T, r *Recorder) *Report {
	t.Helper()
	done := make(chan *Report, 1)
	go func() { done <- r.Report() }()
	select {
	case rep := <-done:
		return rep
	case <-time.After(10 * time.Second):
		t.Fatal("Report hung (the roll loop spun holding the recorder lock)")
		return nil
	}
}

// nonAdvancingInstant finds the first zone and instant where a one-month step
// does not advance: a few historical DST transitions consume a whole month's
// offset change in a single step. It scans each zone with its own calendar, not
// time.Local. It skips when the host tzdata has no such transition rather than
// hard-coding an assumption about the zone database.
func nonAdvancingInstant(t *testing.T) (*time.Location, int64) {
	t.Helper()
	for _, name := range []string{"America/Asuncion", "America/Havana", "America/St_Johns"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		// Hourly scan over a wide window: the anomalies are whole-month offset
		// changes, so anything narrower can miss them.
		start := time.Date(1960, time.January, 1, 0, 0, 0, 0, loc).UnixMilli()
		end := time.Date(2000, time.January, 1, 0, 0, 0, 0, loc).UnixMilli()
		for ms := start; ms < end; ms += int64(time.Hour / time.Millisecond) {
			if addMonthsMsIn(ms, 1, loc) <= ms {
				return loc, ms
			}
		}
	}
	t.Skip("host tzdata has no non-advancing month step; the guard is untestable here")
	return nil, 0
}

// A persisted boundary far in the past must not cost an unbounded loop under the
// recorder lock: the panel polls every 15s and every chat request records usage.
func TestReportBoundsFarPastCatchUp(t *testing.T) {
	r := NewRecorder(nil)
	r.data.NextResetMs = int64(1) // 1970
	r.data.CycleCredits = 7

	done := make(chan *Report, 1)
	go func() { done <- r.Report() }()
	select {
	case rep := <-done:
		if rep.NextResetMs <= time.Now().UnixMilli() {
			t.Errorf("expected a bounded projection to move past now, got %d", rep.NextResetMs)
		}
		if rep.CycleCredits != 0 {
			t.Errorf("expected the stale cycle to project 0 credits, got %v", rep.CycleCredits)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Report did not terminate for a far-past boundary")
	}
}

// An unavailable status (0) must leave existing accounting intact rather than
// wiping it on a transient upstream outage.
func TestSetBillingCycleIgnoresZero(t *testing.T) {
	r := NewRecorder(nil)
	reset := time.Now().Add(72 * time.Hour).UnixMilli()
	r.SetBillingCycle(reset)
	r.RecordUsage(&Usage{Credits: 3})
	r.SetBillingCycle(0)

	rep := r.Report()
	if rep.NextResetMs != reset {
		t.Errorf("zero must not clear the known boundary, got %d", rep.NextResetMs)
	}
	if rep.CycleCredits < 3-1e-9 || rep.CycleCredits > 3+1e-9 {
		t.Errorf("zero must not clear cycle credits, got %v", rep.CycleCredits)
	}
}

// Crossing the reset boundary must zero the cycle exactly once. Regressing
// here (re-zeroing on every record) is what would make the new cycle total
// permanently stuck at the latest single request.
func TestCycleRolloverHappensOnce(t *testing.T) {
	r := NewRecorder(nil)
	// A boundary already in the past: the first record triggers the rollover.
	past := time.Now().Add(-time.Hour).UnixMilli()
	r.SetBillingCycle(past)

	r.RecordUsage(&Usage{Credits: 1})
	r.RecordUsage(&Usage{Credits: 2})
	r.RecordUsage(&Usage{Credits: 4})

	rep := r.Report()
	if rep.CycleCredits < 7-1e-9 || rep.CycleCredits > 7+1e-9 {
		t.Errorf("expected the new cycle to accumulate 7, got %v (rollover fired more than once?)",
			rep.CycleCredits)
	}
	if rep.NextResetMs <= past {
		t.Errorf("expected the boundary to advance past %d, got %d", past, rep.NextResetMs)
	}
	// The advanced boundary must be one calendar month out, so a service that
	// stays up does not roll over again immediately.
	advanced := time.UnixMilli(rep.NextResetMs).Local()
	if d := advanced.Sub(time.UnixMilli(past)); d < 27*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("expected the advanced boundary to be ~1 month later, got %v", d)
	}
	// Lifetime credits are unaffected by the rollover.
	if rep.Credits < 7-1e-9 || rep.Credits > 7+1e-9 {
		t.Errorf("lifetime credits must ignore the rollover, got %v", rep.Credits)
	}
}

// SetAccount must copy its argument: aliasing the caller's pointer would let a
// later mutation reach the persisted snapshot.
func TestSetAccountCopiesAndIsExposed(t *testing.T) {
	r := NewRecorder(nil)
	acct := &Account{
		Plan: "PLAN_TIER_TEAM", Tag: "Teams", IsQuotaExceeded: false,
		UserQuota:          &Quota{Total: 3000, Used: 2939, Remaining: 61, Percentage: 0.98, Unit: "credits"},
		OrgResourcePackage: &OrgResourcePackage{Cap: 4000, Available: false, Unit: "credits"},
	}
	r.SetAccount(acct)
	acct.Tag = "mutated"
	acct.UserQuota.Used = 1
	acct.OrgResourcePackage.Cap = 1

	rep := r.Report()
	if rep.Account == nil {
		t.Fatal("expected the account in the report")
	}
	if rep.Account.Tag != "Teams" {
		t.Errorf("expected the snapshot to be insulated from caller mutation, got %q", rep.Account.Tag)
	}
	if rep.Account.UserQuota == nil || rep.Account.UserQuota.Used != 2939 {
		t.Errorf("expected a deep copy of user quota, got %+v", rep.Account.UserQuota)
	}
	if rep.Account.OrgResourcePackage == nil || rep.Account.OrgResourcePackage.Cap != 4000 {
		t.Errorf("expected a deep copy of organization quota, got %+v", rep.Account.OrgResourcePackage)
	}

	// A nil account must not wipe a known one.
	r.SetAccount(nil)
	if r.Report().Account == nil || r.Report().Account.Tag != "Teams" {
		t.Error("nil account must leave the previous state intact")
	}
}

// ClearAccount drops the cached snapshot (e.g. after a PAT change), unlike
// SetAccount(nil), which is a no-op so an outage cannot wipe a known plan.
func TestClearAccount(t *testing.T) {
	r := NewRecorder(nil)
	r.SetAccount(&Account{Plan: "PLAN_TIER_TEAM", Tag: "Teams"})
	r.dirty = false

	r.ClearAccount()
	if r.Report().Account != nil {
		t.Error("expected the account snapshot to be cleared")
	}
	if !r.dirty {
		t.Error("expected clearing the account to mark the data dirty")
	}

	// Idempotent: an already nil account must not mark the data dirty again.
	r.dirty = false
	r.ClearAccount()
	if r.dirty {
		t.Error("clearing an already nil account must not mark the data dirty")
	}
}

// A read across a boundary must project the rolled cycle instead of mutating
// the recorder, otherwise an admin poll landing just after the boundary would
// persist a rollover that discarded the previous cycle's credits.
func TestReportProjectsCycleOnRead(t *testing.T) {
	r := NewRecorder(nil)
	past := time.Now().Add(-time.Hour).UnixMilli()
	r.SetBillingCycle(past)
	// Seed a cycle total as if it had been accumulated before the boundary.
	r.data.CycleCredits = 9
	r.dirty = false

	rep := r.Report()
	if rep.CycleCredits != 0 {
		t.Errorf("expected the stale cycle total to be projected as 0, got %v", rep.CycleCredits)
	}
	if rep.CycleStartMs != past {
		t.Errorf("expected the projected cycle start %d, got %d", past, rep.CycleStartMs)
	}
	if rep.NextResetMs <= time.Now().UnixMilli() {
		t.Errorf("expected the projected boundary to advance into the future, got %d", rep.NextResetMs)
	}

	// The recorder itself must be untouched: stored values and dirty state.
	if r.data.CycleCredits != 9 {
		t.Errorf("a read mutated the stored cycle credits: %v", r.data.CycleCredits)
	}
	if r.data.NextResetMs != past {
		t.Errorf("a read mutated the stored boundary: %d", r.data.NextResetMs)
	}
	if r.dirty {
		t.Error("a read must not mark the data dirty")
	}

	// A second read must project the same values, not a double advance.
	rep2 := r.Report()
	if rep2.CycleCredits != rep.CycleCredits || rep2.CycleStartMs != rep.CycleStartMs || rep2.NextResetMs != rep.NextResetMs {
		t.Errorf("repeated reads diverged: %+v vs %+v", rep, rep2)
	}
}

// A boundary-crossing read must not force a disk write: a Report right after a
// Flush must not trigger a second SaveStats.
func TestReportAfterFlushDoesNotPersist(t *testing.T) {
	p := &fakePersister{}
	r := NewRecorder(p)
	// A boundary that passed after the last write: the stored cycle still holds
	// credits until the next RecordUsage rolls it.
	r.data.NextResetMs = time.Now().Add(-time.Hour).UnixMilli()
	r.data.CycleStartMs = addMonthsMs(r.data.NextResetMs, -1)
	r.data.CycleCredits = 4
	r.dirty = true
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	rep := r.Report()
	if rep.CycleCredits != 0 {
		t.Errorf("expected the read to project 0 credits for the passed boundary, got %v", rep.CycleCredits)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("second Flush failed: %v", err)
	}
	if p.saveCalls != 1 {
		t.Errorf("a boundary-crossing read forced a second save: %d", p.saveCalls)
	}
}

// A read must never mark the data dirty, otherwise every 15s poll would force a
// disk write.
func TestReportStaysCleanWithoutRollover(t *testing.T) {
	r := NewRecorder(nil)
	r.SetBillingCycle(time.Now().Add(72 * time.Hour).UnixMilli())
	r.dirty = false

	r.Report()
	if r.dirty {
		t.Error("a rollover-free read must not mark the data dirty")
	}
}

// memoryPersister round-trips stats like store.Store does, so a "restart" can
// be modelled as constructing a second Recorder over the same persister.
type memoryPersister struct {
	mu   sync.Mutex
	data *Data
}

func (m *memoryPersister) LoadStats() *Data {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return nil
	}
	return m.data.Clone()
}

func (m *memoryPersister) SaveStats(d *Data) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = d.Clone()
	return nil
}

// A persisted account snapshot outlives the process, so a restart with a
// different (or cleared) PAT would keep showing the previous account: the
// PAT-change hook only fires on an in-process change. main.go therefore clears
// once at startup, and this locks the behaviour that makes that sufficient.
func TestPersistedAccountSurvivesRestartUntilCleared(t *testing.T) {
	p := &memoryPersister{}
	r := NewRecorder(p)
	r.SetAccount(&Account{Plan: "PLAN_A", Tag: "A"})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}

	// A restart loads the snapshot: without an explicit clear it is still shown.
	restarted := NewRecorder(p)
	if got := restarted.Report().Account; got == nil || got.Tag != "A" {
		t.Fatalf("expected the persisted account to be restored, got %+v", got)
	}

	// main.go's startup clear drops it, so a cleared/changed PAT cannot keep
	// serving the previous account's plan and allowance.
	restarted.ClearAccount()
	if got := restarted.Report().Account; got != nil {
		t.Errorf("startup clear left the previous account visible: %+v", got)
	}

	// The clear must be persisted too, or the next restart resurrects it.
	if err := restarted.Flush(); err != nil {
		t.Fatal(err)
	}
	again := NewRecorder(p)
	if got := again.Report().Account; got != nil {
		t.Errorf("the cleared snapshot came back after a second restart: %+v", got)
	}
}
