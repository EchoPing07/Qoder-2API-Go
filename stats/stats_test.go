package stats

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRecordAndReport(t *testing.T) {
	r := NewRecorder(nil)
	r.Record("Qwen3.7-Max", true, true)
	r.Record("Qwen3.7-Max", true, true)
	r.Record("Qwen3.7-Max", false, true)
	r.Record("DeepSeek-V4-Pro", true, false)

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
	if rep.Streams != 3 {
		t.Errorf("expected streams 3, got %d", rep.Streams)
	}
	if rep.Syncs != 1 {
		t.Errorf("expected syncs 1, got %d", rep.Syncs)
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
	r.Record("GLM-5.2", false, true)
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
	r.Record("Kimi-K2.7-Code", true, true)
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
	r.Record("x", true, false)
	if err := r.Flush(); err != nil {
		t.Errorf("Flush with nil persister should not fail, got %v", err)
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
	r.Record("A", true, true)
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}
	r.Record("A", true, true)
	r.Record("B", false, true)
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
	r.Record("M", true, true)
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
				r.Record("Model-"+strconv.Itoa(g), i%3 != 0, true)
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
