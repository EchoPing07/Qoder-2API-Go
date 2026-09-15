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
	reset := time.Date(2026, time.September, 25, 0, 0, 0, 0, time.Local)
	r.SetBillingCycle(reset.UnixMilli())

	rep := r.Report()
	if rep.NextResetMs != reset.UnixMilli() {
		t.Fatalf("expected next reset %d, got %d", reset.UnixMilli(), rep.NextResetMs)
	}
	wantStart := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.Local)
	if rep.CycleStartMs != wantStart.UnixMilli() {
		t.Errorf("expected cycle start %s, got %s",
			wantStart.Format(time.RFC3339), time.UnixMilli(rep.CycleStartMs).Format(time.RFC3339))
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
	acct := &Account{Plan: "PLAN_TIER_TEAM", Tag: "Teams", IsQuotaExceeded: false}
	r.SetAccount(acct)
	acct.Tag = "mutated"

	rep := r.Report()
	if rep.Account == nil {
		t.Fatal("expected the account in the report")
	}
	if rep.Account.Tag != "Teams" {
		t.Errorf("expected the snapshot to be insulated from caller mutation, got %q", rep.Account.Tag)
	}

	// A nil account must not wipe a known one.
	r.SetAccount(nil)
	if r.Report().Account == nil || r.Report().Account.Tag != "Teams" {
		t.Error("nil account must leave the previous state intact")
	}
}

// A read must roll the cycle too, not just a write: the panel polls every 15s,
// so a service sitting idle across the reset boundary would otherwise keep
// reporting the previous cycle's credits.
func TestReportRollsCycleOnRead(t *testing.T) {
	r := NewRecorder(nil)
	r.SetBillingCycle(time.Now().Add(-time.Hour).UnixMilli())
	// Seed a cycle total as if it had been accumulated before the boundary.
	r.data.CycleCredits = 9

	rep := r.Report()
	if rep.CycleCredits != 0 {
		t.Errorf("expected the stale cycle total to be cleared on read, got %v", rep.CycleCredits)
	}
	if rep.NextResetMs <= time.Now().UnixMilli() {
		t.Errorf("expected the boundary to advance into the future, got %d", rep.NextResetMs)
	}
	if !r.dirty {
		t.Error("expected the rollover to mark data dirty so it gets persisted")
	}
}

// A read that does not cross a boundary must not mark the data dirty, otherwise
// every 15s poll would force a disk write.
func TestReportStaysCleanWithoutRollover(t *testing.T) {
	r := NewRecorder(nil)
	r.SetBillingCycle(time.Now().Add(72 * time.Hour).UnixMilli())
	r.dirty = false

	r.Report()
	if r.dirty {
		t.Error("a rollover-free read must not mark the data dirty")
	}
}
