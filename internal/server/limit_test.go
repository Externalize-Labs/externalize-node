package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/v1/status", nil)
	r.RemoteAddr = "10.0.0.7:41234"
	r.Header.Set("X-Real-Ip", " 203.0.113.9 ")
	if got := clientIP(r, ""); got != "10.0.0.7" {
		t.Fatalf("without a header: %q", got)
	}
	if got := clientIP(r, "X-Real-Ip"); got != "203.0.113.9" {
		t.Fatalf("with a header: %q", got)
	}
	r.Header.Del("X-Real-Ip")
	if got := clientIP(r, "X-Real-Ip"); got != "10.0.0.7" {
		t.Fatalf("header configured but absent: %q", got)
	}
}

func TestRateLimitPerClient(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(1, 2)
	l.now = func() time.Time { return now }
	first, second := l.allow("a"), l.allow("a")
	if !first || !second {
		t.Fatal("burst of 2 should pass")
	}
	if l.allow("a") {
		t.Fatal("third request in the same instant should be limited")
	}
	if !l.allow("b") {
		t.Fatal("other clients are unaffected")
	}
	now = now.Add(time.Second)
	if !l.allow("a") {
		t.Fatal("one token should refill after a second")
	}
}
