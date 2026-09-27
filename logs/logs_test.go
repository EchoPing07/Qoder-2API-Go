package logs

import (
	"testing"
	"time"
)

// fakePersister is an in-memory Persister for tests.
type fakePersister struct {
	saved   []LogEntry
	load    []LogEntry
	loadSet bool
}

func (p *fakePersister) LoadLogs() []LogEntry { return p.load }
func (p *fakePersister) SaveLogs(e []LogEntry) error {
	p.saved = append([]LogEntry(nil), e...)
	return nil
}

func mkEntry(model string, created int64) *LogEntry {
	return &LogEntry{Model: model, CreatedAt: created}
}

func TestRecordAssignsIDAndTimestamp(t *testing.T) {
	r := NewRecorder(nil, 90, 2000)
	now := time.Now().Unix()
	r.Record(mkEntry("m1", 0))
	r.Record(mkEntry("m2", 0))
	res := r.Query(Filter{})
	if res.Total != 2 {
		t.Fatalf("total = %d, want 2", res.Total)
	}
	// Newest first
	if res.Logs[0].Model != "m2" || res.Logs[1].Model != "m1" {
		t.Errorf("order = %s,%s; want newest first", res.Logs[0].Model, res.Logs[1].Model)
	}
	if res.Logs[0].ID <= res.Logs[1].ID {
		t.Errorf("IDs must increase: %d then %d", res.Logs[1].ID, res.Logs[0].ID)
	}
	// IDs start at 1, not 0: a zero ID is indistinguishable from an unset
	// field in the JSON payload and breaks the UI's :key binding.
	if res.Logs[1].ID < 1 {
		t.Errorf("first assigned ID = %d, want >= 1", res.Logs[1].ID)
	}
	if res.Logs[0].CreatedAt < now {
		t.Errorf("CreatedAt not assigned: %d", res.Logs[0].CreatedAt)
	}
}

func TestRecordNilEntryIsNoop(t *testing.T) {
	r := NewRecorder(nil, 90, 2000)
	r.Record(nil)
	if r.Len() != 0 {
		t.Fatalf("len = %d, want 0", r.Len())
	}
}

func TestQueryFilters(t *testing.T) {
	r := NewRecorder(nil, 90, 2000)
	// Base a live instant: explicit CreatedAt values must survive the
	// retention window (a 1970 timestamp would be pruned on insert).
	base := time.Now().Unix()
	r.Record(&LogEntry{Model: "Qwen3.7-Max", KeyID: "k1", StatusCode: 200, CreatedAt: base + 1})
	r.Record(&LogEntry{Model: "DeepSeek-V4-Pro", KeyID: "k2", StatusCode: 400, ErrorMsg: "bad param", CreatedAt: base + 2})
	r.Record(&LogEntry{Model: "Qwen3.7-Plus", KeyID: "k1", StatusCode: 200, CreatedAt: base + 3})

	cases := []struct {
		name  string
		f     Filter
		model []string // models of matched entries, newest first
	}{
		{"model substring case-insensitive", Filter{Model: "qwen"}, []string{"Qwen3.7-Plus", "Qwen3.7-Max"}},
		{"key id exact", Filter{KeyID: "k1"}, []string{"Qwen3.7-Plus", "Qwen3.7-Max"}},
		{"status exact", Filter{Status: 400}, []string{"DeepSeek-V4-Pro"}},
		{"errors only includes 4xx and msg", Filter{Status: -1}, []string{"DeepSeek-V4-Pro"}},
		{"combined", Filter{Model: "qwen", KeyID: "k1", Status: 200}, []string{"Qwen3.7-Plus", "Qwen3.7-Max"}},
		{"no match", Filter{Model: "gpt"}, nil},
	}
	for _, c := range cases {
		res := r.Query(c.f)
		if res.Total != len(c.model) {
			t.Errorf("%s: total = %d, want %d", c.name, res.Total, len(c.model))
			continue
		}
		for i, want := range c.model {
			if res.Logs[i].Model != want {
				t.Errorf("%s: entry %d model = %s, want %s", c.name, i, res.Logs[i].Model, want)
			}
		}
	}
}

func TestQueryPagination(t *testing.T) {
	r := NewRecorder(nil, 90, 2000)
	base := time.Now().Unix()
	for i := 0; i < 25; i++ {
		r.Record(mkEntry("m", base+int64(i+1)))
	}
	res := r.Query(Filter{Page: 1, PageSize: 20})
	if len(res.Logs) != 20 || res.Total != 25 || res.Page != 1 || res.PageSize != 20 {
		t.Fatalf("page1: len=%d total=%d", len(res.Logs), res.Total)
	}
	// Newest first: page 1 ends at entry created=base+6
	if res.Logs[19].CreatedAt != base+6 {
		t.Errorf("page1 last entry created = %d, want %d", res.Logs[19].CreatedAt, base+6)
	}
	res = r.Query(Filter{Page: 2, PageSize: 20})
	if len(res.Logs) != 5 {
		t.Fatalf("page2: len = %d, want 5", len(res.Logs))
	}
	if res.Logs[0].CreatedAt != base+5 {
		t.Errorf("page2 first entry created = %d, want %d", res.Logs[0].CreatedAt, base+5)
	}
	// Out-of-range page yields an empty (non-nil) page, not a crash.
	res = r.Query(Filter{Page: 99, PageSize: 20})
	if res.Logs == nil || len(res.Logs) != 0 {
		t.Errorf("out-of-range page: logs = %v, want empty non-nil", res.Logs)
	}
	// Defaults: page<1 → 1, pagesize<1 or >100 → 20.
	res = r.Query(Filter{})
	if res.Page != 1 || res.PageSize != 20 {
		t.Errorf("defaults: page=%d pagesize=%d", res.Page, res.PageSize)
	}
}

func TestEntryCapTrimsOldest(t *testing.T) {
	// Entry cap is clamped to at least MinMaxEntries, so drive the cap via
	// SetLimits after construction to keep the test small.
	r := NewRecorder(nil, 90, DefaultMaxEntries)
	r.SetLimits(90, MinMaxEntries)
	base := time.Now().Unix()
	for i := 0; i < MinMaxEntries+10; i++ {
		r.Record(mkEntry("m", base+int64(i+1)))
	}
	if r.Len() != MinMaxEntries {
		t.Fatalf("len = %d, want %d", r.Len(), MinMaxEntries)
	}
	res := r.Query(Filter{Page: 1, PageSize: 1})
	if res.Logs[0].CreatedAt != base+int64(MinMaxEntries+10) {
		t.Errorf("newest entry created = %d, want %d", res.Logs[0].CreatedAt, base+MinMaxEntries+10)
	}
}

func TestRetentionPrune(t *testing.T) {
	r := NewRecorder(nil, 90, 2000)
	now := time.Now()
	old := now.Add(-48 * time.Hour).Unix()
	r.Record(mkEntry("old", old))
	r.Record(mkEntry("new", now.Unix()))
	r.SetLimits(1, 2000) // 1-day retention drops "old"
	if r.Len() != 1 {
		t.Fatalf("len = %d, want 1 (expired entry not pruned)", r.Len())
	}
	res := r.Query(Filter{})
	if res.Logs[0].Model != "new" {
		t.Errorf("surviving entry = %s, want new", res.Logs[0].Model)
	}
}

func TestFlushPersistsCopy(t *testing.T) {
	p := &fakePersister{}
	r := NewRecorder(p, 90, 2000)
	r.Record(mkEntry("m1", time.Now().Unix()))
	if err := r.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(p.saved) != 1 {
		t.Fatalf("saved %d entries, want 1", len(p.saved))
	}
	// Mutating the saved slice must not alias the recorder's entries.
	p.saved[0].Model = "mutated"
	res := r.Query(Filter{})
	if res.Logs[0].Model != "m1" {
		t.Errorf("recorder aliased persisted slice: model = %s", res.Logs[0].Model)
	}
	// A second flush with no changes must not rewrite.
	before := p.saved
	r.Record(mkEntry("m2", time.Now().Unix()))
	if err := r.Flush(); err != nil {
		t.Fatalf("flush2: %v", err)
	}
	if len(p.saved) != 2 {
		t.Fatalf("saved %d entries after 2nd flush, want 2", len(p.saved))
	}
	if &before[0] == &p.saved[0] && len(before) == 2 {
		t.Error("unchanged flush rewrote the slice")
	}
}

func TestNewRecorderRestoresAndReprunes(t *testing.T) {
	p := &fakePersister{}
	p.load = []LogEntry{
		{ID: 7, Model: "a", CreatedAt: time.Now().Add(-48 * time.Hour).Unix()},
		{ID: 3, Model: "b", CreatedAt: time.Now().Unix()},
	}
	// 1-day retention: the 2-day-old entry must be pruned at construction.
	r := NewRecorder(p, 1, 2000)
	if r.Len() != 1 {
		t.Fatalf("len = %d, want 1", r.Len())
	}
	// IDs must continue past the restored maximum.
	r.Record(mkEntry("c", time.Now().Unix()))
	res := r.Query(Filter{PageSize: 1})
	if res.Logs[0].ID <= 7 {
		t.Errorf("new entry ID = %d, want > 7", res.Logs[0].ID)
	}
}

func TestLimitNormalization(t *testing.T) {
	r := NewRecorder(nil, 0, 0) // both invalid → defaults
	if r.retention != DefaultRetentionDays*24*time.Hour {
		t.Errorf("retention = %v, want default %dd", r.retention, DefaultRetentionDays)
	}
	if r.maxEntries != DefaultMaxEntries {
		t.Errorf("maxEntries = %d, want default %d", r.maxEntries, DefaultMaxEntries)
	}
	r.SetLimits(MaxRetentionDays+100, MaxEntriesHardCap+100)
	if r.retention != MaxRetentionDays*24*time.Hour {
		t.Errorf("retention = %v, want clamp %dd", r.retention, MaxRetentionDays)
	}
	if r.maxEntries != MaxEntriesHardCap {
		t.Errorf("maxEntries = %d, want clamp %d", r.maxEntries, MaxEntriesHardCap)
	}
}

func TestQueryReturnsEmptySliceForEmptyLog(t *testing.T) {
	r := NewRecorder(nil, 90, 2000)
	res := r.Query(Filter{})
	if res.Logs == nil {
		t.Error("Logs must be an empty slice, not nil (JSON encodes nil as null)")
	}
}
