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

// Data is the persisted stats snapshot.
type Data struct {
	Total   int64                 `json:"total"`
	Success int64                 `json:"success"`
	Failed  int64                 `json:"failed"`
	Streams int64                 `json:"streams"`
	Syncs   int64                 `json:"syncs"`
	ByModel map[string]*ModelStat `json:"by_model,omitempty"`
	Hourly  map[string]*HourStat  `json:"hourly,omitempty"`
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
	Total       int64      `json:"total"`
	Success     int64      `json:"success"`
	Failed      int64      `json:"failed"`
	Streams     int64      `json:"streams"`
	Syncs       int64      `json:"syncs"`
	SuccessRate float64    `json:"success_rate"`
	ByModel     []ModelRow `json:"by_model"`
	Hourly      []HourRow  `json:"hourly"`
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
			r.data = normalize(d).clone()
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

// clone returns a deep copy of d. Callers must not hold the recorder mutex
// when the copy is needed for concurrent use.
func (d *Data) clone() *Data {
	cp := &Data{
		Total:   d.Total,
		Success: d.Success,
		Failed:  d.Failed,
		Streams: d.Streams,
		Syncs:   d.Syncs,
		ByModel: make(map[string]*ModelStat, len(d.ByModel)),
		Hourly:  make(map[string]*HourStat, len(d.Hourly)),
	}
	for k, v := range d.ByModel {
		cp.ByModel[k] = &ModelStat{Model: v.Model, Total: v.Total, Success: v.Success, Failed: v.Failed}
	}
	for k, v := range d.Hourly {
		cp.Hourly[k] = &HourStat{Hour: v.Hour, Total: v.Total, Success: v.Success, Failed: v.Failed}
	}
	return cp
}

// Record counts one completed request.
func (r *Recorder) Record(model string, ok, stream bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.dirty = true
	r.data.Total++
	if ok {
		r.data.Success++
	} else {
		r.data.Failed++
	}
	if stream {
		r.data.Streams++
	} else {
		r.data.Syncs++
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
	cp := r.data.clone()
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

	rep := &Report{
		Total:   r.data.Total,
		Success: r.data.Success,
		Failed:  r.data.Failed,
		Streams: r.data.Streams,
		Syncs:   r.data.Syncs,
		ByModel: []ModelRow{},
		Hourly:  []HourRow{},
	}
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
