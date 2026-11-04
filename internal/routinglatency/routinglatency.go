// Package routinglatency is the Go counterpart of AxonRouter's
// src/lib/routingLatency.ts: an in-process ring buffer of wall-clock times
// for requests routed by the gateway, summarized as percentiles over a
// sliding window. In-memory only by design — the numbers describe how the
// router is behaving right now, not a historical record.
package routinglatency

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	defaultWindowMs = 5 * 60 * 1000
	maxSamples      = 2048
)

type sample struct {
	t          int64 // epoch ms
	ms         float64
	status     string
	providerID string
}

var (
	mu              sync.Mutex
	samples         []sample
	cursor          int
	totalCount      int
	totalErrorCount int
	lastMs          *float64
	lastAt          int64
)

// Record adds one routing sample. Non-finite or negative durations drop,
// as upstream.
func Record(ms float64, providerID, status string) {
	if ms < 0 {
		return
	}
	if status == "" {
		status = "ok"
	}
	s := sample{t: time.Now().UnixMilli(), ms: ms, status: status, providerID: providerID}
	mu.Lock()
	defer mu.Unlock()
	if len(samples) < maxSamples {
		samples = append(samples, s)
	} else {
		samples[cursor] = s
		cursor = (cursor + 1) % maxSamples
	}
	totalCount++
	if status != "ok" {
		totalErrorCount++
	}
	v := ms
	lastMs = &v
	lastAt = s.t
}

func quantile(sorted []float64, q float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	if len(sorted) == 1 {
		v := sorted[0]
		return &v
	}
	pos := float64(len(sorted)-1) * q
	lo := int(pos)
	hi := lo
	if float64(lo) < pos {
		hi = lo + 1
	}
	if lo == hi {
		v := sorted[lo]
		return &v
	}
	v := sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
	return &v
}

func numOrNil(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// Summary mirrors getRoutingLatencySummary: windowed percentiles plus the
// process-lifetime counters. windowMs <= 0 selects the 5-minute default.
func Summary(windowMs int64) map[string]any {
	if windowMs <= 0 {
		windowMs = defaultWindowMs
	}
	now := time.Now().UnixMilli()
	cutoff := now - windowMs

	mu.Lock()
	defer mu.Unlock()
	windowed := make([]float64, 0, len(samples))
	var earliest *int64
	errorCount := 0
	for _, s := range samples {
		if s.t < cutoff {
			continue
		}
		if earliest == nil || s.t < *earliest {
			v := s.t
			earliest = &v
		}
		if s.status != "ok" {
			errorCount++
		}
		windowed = append(windowed, s.ms)
	}
	sort.Float64s(windowed)
	count := len(windowed)
	var avg *float64
	var minV, maxV any
	if count > 0 {
		sum := 0.0
		for _, ms := range windowed {
			sum += ms
		}
		a := sum / float64(count)
		avg = &a
		minV = windowed[0]
		maxV = windowed[count-1]
	}
	var sampledFrom any
	if earliest != nil {
		sampledFrom = *earliest
	}
	return map[string]any{
		"p50":             numOrNil(quantile(windowed, 0.5)),
		"p95":             numOrNil(quantile(windowed, 0.95)),
		"p99":             numOrNil(quantile(windowed, 0.99)),
		"avg":             numOrNil(avg),
		"min":             minV,
		"max":             maxV,
		"count":           count,
		"errorCount":      errorCount,
		"totalCount":      totalCount,
		"totalErrorCount": totalErrorCount,
		"lastMs":          numOrNil(lastMs),
		"lastAt":          lastAt,
		"windowMs":        windowMs,
		"sampledFromMs":   sampledFrom,
	}
}

// statusRecorder captures the written status code for the latency sample.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Flush preserves streaming behaviour for wrapped SSE handlers.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Wrap times a gateway request handler the way upstream's measureRouting
// wraps the /v1 route handlers, recording the full handling time. The
// status reads "error" when the response status is 5xx, else "ok" —
// upstream counts a sample as error only when the handler throws, and a
// Go handler surfaces its failures as 5xx responses instead.
func Wrap(providerID string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		status := "ok"
		if rec.status >= 500 {
			status = "error"
		}
		Record(float64(time.Since(start).Microseconds())/1000.0, providerID, status)
	}
}
