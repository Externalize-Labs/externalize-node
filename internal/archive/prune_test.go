package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneRemovesOldestUntilUnderBudget(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, age time.Duration) {
		p := filepath.Join(dir, "scp", name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		_ = os.Chtimes(p, when, when)
	}
	write("old.xdr.gz", 100, 3*time.Hour)
	write("mid.xdr.gz", 100, 2*time.Hour)
	write("new.xdr.gz", 100, time.Hour)
	write("notes.txt", 1000, 5*time.Hour) // not a checkpoint file; never touched

	res, err := Prune(dir, 150)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 3 || res.Removed != 2 || res.Before != 300 || res.After != 100 {
		t.Fatalf("unexpected result %+v", res)
	}
	for name, want := range map[string]bool{"old.xdr.gz": false, "mid.xdr.gz": false, "new.xdr.gz": true, "notes.txt": true} {
		_, err := os.Stat(filepath.Join(dir, "scp", name))
		if (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, want)
		}
	}
}
