package server

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	mu      sync.Mutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

func newHistogram(buckets []float64) *histogram {
	return &histogram{
		buckets: buckets,
		counts:  make([]uint64, len(buckets)),
	}
}

func (h *histogram) observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.count++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

// metrics are exported at GET /metrics in the Prometheus text format.
type metrics struct {
	mu            sync.Mutex
	requests      map[string]*atomic.Uint64 // by status class: 2xx, 4xx, 5xx
	built         atomic.Uint64
	failed        atomic.Uint64
	duration      map[string]*histogram
	archiveHits   func() uint64
	archiveMisses func() uint64
}

func newMetrics() *metrics {
	return &metrics{
		requests: map[string]*atomic.Uint64{},
		duration: map[string]*histogram{},
	}
}

func (m *metrics) observeDuration(kind string, d time.Duration) {
	secs := d.Seconds()
	m.mu.Lock()
	h, ok := m.duration[kind]
	if !ok {
		h = newHistogram(defaultBuckets)
		m.duration[kind] = h
	}
	m.mu.Unlock()
	h.observe(secs)
}

func (m *metrics) request(status int) {
	class := fmt.Sprintf("%dxx", status/100)
	m.mu.Lock()
	c, ok := m.requests[class]
	if !ok {
		c = new(atomic.Uint64)
		m.requests[class] = c
	}
	m.mu.Unlock()
	c.Add(1)
}

func (m *metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP exnode_http_requests_total HTTP requests served, by status class.")
	fmt.Fprintln(w, "# TYPE exnode_http_requests_total counter")
	m.mu.Lock()
	classes := make([]string, 0, len(m.requests))
	for k := range m.requests {
		classes = append(classes, k)
	}
	sort.Strings(classes)
	for _, k := range classes {
		fmt.Fprintf(w, "exnode_http_requests_total{class=%q} %d\n", k, m.requests[k].Load())
	}
	m.mu.Unlock()

	fmt.Fprintln(w, "# HELP exnode_bundles_built_total Bundles built successfully.")
	fmt.Fprintln(w, "# TYPE exnode_bundles_built_total counter")
	fmt.Fprintf(w, "exnode_bundles_built_total %d\n", m.built.Load())

	fmt.Fprintln(w, "# HELP exnode_bundles_failed_total Bundle requests that failed.")
	fmt.Fprintln(w, "# TYPE exnode_bundles_failed_total counter")
	fmt.Fprintf(w, "exnode_bundles_failed_total %d\n", m.failed.Load())

	// Bundle build duration histogram by kind
	fmt.Fprintln(w, "# HELP exnode_bundle_build_duration_seconds Latency of bundle builds in seconds.")
	fmt.Fprintln(w, "# TYPE exnode_bundle_build_duration_seconds histogram")
	m.mu.Lock()
	kinds := make([]string, 0, len(m.duration))
	for k := range m.duration {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		h := m.duration[kind]
		h.mu.Lock()
		for i, b := range h.buckets {
			fmt.Fprintf(w, "exnode_bundle_build_duration_seconds_bucket{kind=%q,le=\"%g\"} %d\n", kind, b, h.counts[i])
		}
		fmt.Fprintf(w, "exnode_bundle_build_duration_seconds_bucket{kind=%q,le=\"+Inf\"} %d\n", kind, h.count)
		fmt.Fprintf(w, "exnode_bundle_build_duration_seconds_sum{kind=%q} %g\n", kind, h.sum)
		fmt.Fprintf(w, "exnode_bundle_build_duration_seconds_count{kind=%q} %d\n", kind, h.count)
		h.mu.Unlock()
	}
	m.mu.Unlock()

	// Archive cache hit/miss counters
	if m.archiveHits != nil {
		fmt.Fprintln(w, "# HELP exnode_archive_cache_hits_total Checkpoint files served from local cache.")
		fmt.Fprintln(w, "# TYPE exnode_archive_cache_hits_total counter")
		fmt.Fprintf(w, "exnode_archive_cache_hits_total %d\n", m.archiveHits())
	}
	if m.archiveMisses != nil {
		fmt.Fprintln(w, "# HELP exnode_archive_cache_misses_total Checkpoint files fetched from remote archive mirrors.")
		fmt.Fprintln(w, "# TYPE exnode_archive_cache_misses_total counter")
		fmt.Fprintf(w, "exnode_archive_cache_misses_total %d\n", m.archiveMisses())
	}
}
