package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-client token bucket: each client may burst up to
// Burst requests and then refills at PerSecond.
type rateLimiter struct {
	PerSecond float64
	Burst     float64

	mu      sync.Mutex
	clients map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perSecond, burst float64) *rateLimiter {
	return &rateLimiter{PerSecond: perSecond, Burst: burst, clients: map[string]*bucket{}, now: time.Now}
}

func (l *rateLimiter) allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.clients[client]
	if !ok {
		if len(l.clients) > 100_000 {
			l.clients = map[string]*bucket{} // bound memory under address churn
		}
		b = &bucket{tokens: l.Burst, last: now}
		l.clients[client] = b
	}
	b.tokens = min(l.Burst, b.tokens+now.Sub(b.last).Seconds()*l.PerSecond)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP identifies the client for rate limiting: the value of header when
// one is configured and present, otherwise the connection's remote address.
//
// Only name a header that the proxy in front of exnode sets on every request,
// replacing whatever the client sent (Vercel's X-Real-Ip, for example). A
// header the client controls, such as a plain X-Forwarded-For, lets it claim a
// fresh identity on every request; without such a proxy, leave header empty.
func clientIP(r *http.Request, header string) string {
	if header != "" {
		if v := strings.TrimSpace(r.Header.Get(header)); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func withRateLimit(l *rateLimiter, header string, next http.Handler) http.Handler {
	if l == nil || l.PerSecond <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") || l.allow(clientIP(r, header)) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
	})
}

// withCORS lets browser wallets on the allowed origins fetch bundles.
// "*" allows any origin; bundles are public data and requests carry no credentials.
func withCORS(origins []string, next http.Handler) http.Handler {
	if len(origins) == 0 {
		return next
	}
	any := false
	allowed := map[string]bool{}
	for _, o := range origins {
		if o == "*" {
			any = true
		}
		allowed[strings.TrimRight(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (any || allowed[origin]) {
			if any {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
