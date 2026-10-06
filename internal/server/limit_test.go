package server

import (
	"testing"
	"time"
)

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
