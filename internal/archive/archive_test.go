package archive

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckpoint(t *testing.T) {
	for seq, want := range map[uint32]uint32{1: 63, 63: 63, 64: 127, 64791296: 64791359, 64791359: 64791359} {
		if got := Checkpoint(seq); got != want {
			t.Errorf("Checkpoint(%d) = %d, want %d", seq, got, want)
		}
	}
}

func TestPath(t *testing.T) {
	if got, want := Path(SCP, 64791359), "scp/03/dc/a3/scp-03dca33f.xdr.gz"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFramesRejectsTruncation(t *testing.T) {
	good := []byte{0x80, 0, 0, 2, 'h', 'i'}
	if recs, err := Frames(good); err != nil || len(recs) != 1 || string(recs[0]) != "hi" {
		t.Fatalf("Frames(good) = %q, %v", recs, err)
	}
	for name, bad := range map[string][]byte{
		"short mark":     {0x80, 0},
		"short record":   {0x80, 0, 0, 9, 'x'},
		"multi-fragment": {0x00, 0, 0, 1, 'x'},
	} {
		if _, err := Frames(bad); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCheckpointFilesAreCachedForever(t *testing.T) {
	var hits atomic.Int32
	fs := http.FileServer(http.Dir("../../testdata/archive"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fs.ServeHTTP(w, r)
	}))
	defer srv.Close()

	cache := t.TempDir()
	c := NewClient(srv.URL, cache)
	c.SetMemoryFiles(0) // exercise the disk cache, not the memory one
	for range 3 {
		l, err := c.Ledger(context.Background(), 64791359, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Header) == 0 || len(l.SCP) == 0 || len(l.Results) == 0 {
			t.Fatal("ledger records missing")
		}
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("expected one download per category (3), got %d", n)
	}
	if _, err := os.Stat(filepath.Join(cache, "scp", "03", "dc", "a3", "scp-03dca33f.xdr.gz")); err != nil {
		t.Fatal("cache file not written at archive path")
	}
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/archive/scp/03/dc/a3/scp-03dca33f.xdr.gz")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func server(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func fast(c *Client) *Client {
	c.Backoff = time.Millisecond
	return c
}

func TestRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	good := fixture(t)
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(good)
	})
	if _, err := fast(NewClient(url, "")).File(context.Background(), SCP, 64791359); err != nil {
		t.Fatalf("third attempt should succeed: %v", err)
	}
}

func TestFailsOverToTheNextMirror(t *testing.T) {
	good := fixture(t)
	broken := server(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) })
	corrupt := server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not gzip")) })
	healthy := server(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(good) })
	c := fast(NewClient(broken+","+corrupt+" , "+healthy+"/", ""))
	if len(c.Mirrors) != 3 {
		t.Fatalf("mirrors parsed as %v", c.Mirrors)
	}
	if _, err := c.File(context.Background(), SCP, 64791359); err != nil {
		t.Fatalf("healthy mirror should be used: %v", err)
	}
}

func TestNotPublishedOnlyWhenEveryMirrorSaysSo(t *testing.T) {
	missing := func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	c := fast(NewClient(server(t, missing)+","+server(t, missing), ""))
	if _, err := c.File(context.Background(), SCP, 64791359); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("want ErrNotPublished, got %v", err)
	}
	down := server(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusInternalServerError) })
	c = fast(NewClient(server(t, missing)+","+down, ""))
	if _, err := c.File(context.Background(), SCP, 64791359); err == nil || errors.Is(err, ErrNotPublished) {
		t.Fatalf("a mirror that is down is not proof of absence: %v", err)
	}
}

func TestGivesUpWhenTheContextEnds(t *testing.T) {
	url := server(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) })
	c := NewClient(url, "")
	c.Backoff = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.File(ctx, SCP, 64791359); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestRepeatedLedgersComeFromMemory(t *testing.T) {
	var hits atomic.Int32
	fs := http.FileServer(http.Dir("../../testdata/archive"))
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fs.ServeHTTP(w, r)
	})
	c := NewClient(url, "")
	for range 5 {
		if _, err := c.Ledger(context.Background(), 64791359, false); err != nil {
			t.Fatal(err)
		}
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("expected 3 downloads, then memory hits; got %d downloads", n)
	}
}

func TestLRUEvictsTheLeastRecentlyUsed(t *testing.T) {
	l := newLRU(2)
	l.put("a", nil)
	l.put("b", nil)
	l.get("a")
	l.put("c", nil)
	if _, ok := l.get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := l.get(k); !ok {
			t.Fatalf("%s should still be cached", k)
		}
	}
}
