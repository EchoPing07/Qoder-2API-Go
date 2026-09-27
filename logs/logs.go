// Package logs provides request logging (metadata only, never conversation
// content) with filtering, pagination, retention pruning and lazy persistence.
//
// The design mirrors stats.Recorder: entries are appended in memory on the
// request path and persisted out of band via Flush. The persister is
// store.Store, which keeps logs in the same data.json as the rest of the
// config — the JSON equivalent of Buddy-2API's logs table.
package logs

import (
	"sync"
	"time"
)

// LogEntry is one completed request's audit record. Only metadata is stored:
// no prompts, no completions, no key material.
type LogEntry struct {
	ID               int64   `json:"id"`
	KeyID            string  `json:"key_id,omitempty"`
	KeyNote          string  `json:"key_note,omitempty"`
	Model            string  `json:"model,omitempty"`
	Stream           bool    `json:"stream,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	Credits          float64 `json:"credits,omitempty"`
	DurationMs       int64   `json:"duration_ms,omitempty"`
	StatusCode       int     `json:"status_code,omitempty"`
	ErrorMsg         string  `json:"error_msg,omitempty"`
	CreatedAt        int64   `json:"created_at"`
}

// Filter is a log query. Model matches as a substring, KeyID exactly. For
// Status, >0 matches that client status code, <0 matches "errors only"
// (>= 400 or a non-empty error message) and 0 disables the filter.
type Filter struct {
	Model    string
	KeyID    string
	Status   int
	Page     int
	PageSize int
}

// Result is a page of query output. Entries are newest first.
type Result struct {
	Logs     []LogEntry `json:"logs"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

// Persister persists the log slice (implemented by store.Store).
type Persister interface {
	LoadLogs() []LogEntry
	SaveLogs([]LogEntry) error
}

// Retention / capacity bounds. The entry cap is the JSON-storage equivalent of
// Buddy-2API's log size cap: without it, the slice would grow data.json
// (rewritten on every flush) without limit.
const (
	DefaultRetentionDays = 90
	MaxRetentionDays     = 3650
	MinMaxEntries        = 100
	DefaultMaxEntries    = 2000
	MaxEntriesHardCap    = 100000
)

// Recorder aggregates log entries with thread-safe access.
type Recorder struct {
	mu         sync.Mutex
	entries    []LogEntry
	nextID     int64
	persist    Persister
	dirty      bool
	retention  time.Duration
	maxEntries int
}

// NewRecorder creates a log recorder, restoring persisted entries and
// re-pruning them against the current limits (retention may have elapsed while
// the service was down). p may be nil (no persistence; tests).
func NewRecorder(p Persister, retentionDays, maxEntries int) *Recorder {
	// IDs start at 1 (Buddy-2API's AUTOINCREMENT semantics): a zero ID would
	// be indistinguishable from an unset field in JSON and in the UI's :key.
	r := &Recorder{
		nextID:     1,
		persist:    p,
		retention:  normalizeRetention(retentionDays),
		maxEntries: normalizeMaxEntries(maxEntries),
	}
	if p != nil {
		if entries := p.LoadLogs(); len(entries) > 0 {
			r.entries = append([]LogEntry(nil), entries...)
			for _, e := range r.entries {
				if e.ID >= r.nextID {
					r.nextID = e.ID + 1
				}
			}
			r.pruneLocked(time.Now())
			// The prune above may have dropped entries; persist the trimmed
			// state so a crash before the next flush cannot resurrect them.
			r.dirty = true
		}
	}
	return r
}

// Record appends one entry. ID and CreatedAt are assigned here when unset,
// so callers can build a bare entry and mutate it until the deferred Record.
func (r *Recorder) Record(e *LogEntry) {
	if e == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirty = true
	e.ID = r.nextID
	r.nextID++
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	r.entries = append(r.entries, *e)
	r.pruneLocked(time.Now())
}

// SetLimits updates retention and the entry cap, pruning immediately so a
// lowered limit takes effect at once rather than at the next flush.
func (r *Recorder) SetLimits(retentionDays, maxEntries int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retention = normalizeRetention(retentionDays)
	r.maxEntries = normalizeMaxEntries(maxEntries)
	before := len(r.entries)
	r.pruneLocked(time.Now())
	if len(r.entries) != before {
		r.dirty = true
	}
}

// Limits reports the effective retention (in days) and entry cap — the values
// actually enforced, env startup overrides included — which is what the admin
// panel should display (mirrors auth.CurrentStreamTimeouts for the timeouts).
func (r *Recorder) Limits() (retentionDays, maxEntries int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int(r.retention / (24 * time.Hour)), r.maxEntries
}

// Query returns one page of entries matching the filter, newest first.
func (r *Recorder) Query(f Filter) Result {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	matched := make([]LogEntry, 0, len(r.entries))
	// Reverse walk: entries are chronological, the page shows newest first.
	for i := len(r.entries) - 1; i >= 0; i-- {
		e := r.entries[i]
		if f.Model != "" && !containsFold(e.Model, f.Model) {
			continue
		}
		if f.KeyID != "" && e.KeyID != f.KeyID {
			continue
		}
		if f.Status > 0 && e.StatusCode != f.Status {
			continue
		}
		if f.Status < 0 && e.StatusCode < 400 && e.ErrorMsg == "" {
			continue
		}
		matched = append(matched, e)
	}

	total := len(matched)
	start := (f.Page - 1) * f.PageSize
	if start > total {
		start = total
	}
	end := start + f.PageSize
	if end > total {
		end = total
	}
	page := append([]LogEntry(nil), matched[start:end]...)
	if page == nil {
		page = []LogEntry{}
	}
	return Result{Logs: page, Total: total, Page: f.Page, PageSize: f.PageSize}
}

// Flush persists pending entries if anything changed since the last flush.
// A copy is saved so concurrent Record calls never alias the persisted slice.
func (r *Recorder) Flush() error {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	cp := append([]LogEntry(nil), r.entries...)
	r.dirty = false
	r.mu.Unlock()

	if r.persist == nil {
		return nil
	}
	if err := r.persist.SaveLogs(cp); err != nil {
		r.mu.Lock()
		r.dirty = true
		r.mu.Unlock()
		return err
	}
	return nil
}

// Len reports the number of retained entries (mainly for tests and probes).
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// pruneLocked drops entries past the retention window and trims the slice to
// the entry cap. Both only ever remove from the front: entries are appended in
// chronological order, so the first survivor ends the retention scan. Caller
// must hold the mutex.
func (r *Recorder) pruneLocked(now time.Time) {
	if r.retention > 0 {
		cutoff := now.Add(-r.retention).Unix()
		drop := 0
		for drop < len(r.entries) && r.entries[drop].CreatedAt < cutoff {
			drop++
		}
		if drop > 0 {
			r.entries = append(r.entries[:0], r.entries[drop:]...)
		}
	}
	if r.maxEntries > 0 && len(r.entries) > r.maxEntries {
		excess := len(r.entries) - r.maxEntries
		r.entries = append(r.entries[:0], r.entries[excess:]...)
	}
}

// normalizeRetention maps invalid day counts to the default and bounds the
// rest, so a hand-edited data.json cannot disable retention by zero.
func normalizeRetention(days int) time.Duration {
	if days <= 0 {
		days = DefaultRetentionDays
	}
	if days > MaxRetentionDays {
		days = MaxRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// normalizeMaxEntries clamps the entry cap into its valid range.
func normalizeMaxEntries(n int) int {
	if n <= 0 {
		return DefaultMaxEntries
	}
	if n < MinMaxEntries {
		return MinMaxEntries
	}
	if n > MaxEntriesHardCap {
		return MaxEntriesHardCap
	}
	return n
}

// containsFold reports whether s contains sub case-insensitively.
func containsFold(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

// equalFold is an ASCII-only case-insensitive comparison, sufficient for model
// names, and avoids the allocation of strings.ToLower per candidate.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
