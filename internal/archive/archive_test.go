package archive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
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
