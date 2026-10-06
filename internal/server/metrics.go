package server

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// metrics are exported at GET /metrics in the Prometheus text format.
type metrics struct {
	mu       sync.Mutex
	requests map[string]*atomic.Uint64 // by status class: 2xx, 4xx, 5xx
	built    atomic.Uint64
	failed   atomic.Uint64
}

func newMetrics() *metrics {
	return &metrics{requests: map[string]*atomic.Uint64{}}
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
}
