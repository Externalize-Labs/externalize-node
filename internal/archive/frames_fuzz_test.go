package archive

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzFrames checks that any input either parses into records that exactly
// cover it, or fails cleanly; it never panics or over-reads.
func FuzzFrames(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x80, 0, 0, 2, 'h', 'i'})
	f.Add([]byte{0x80, 0, 0, 9, 'x'})
	f.Add([]byte{0x00, 0, 0, 1, 'x'})
	f.Fuzz(func(t *testing.T, data []byte) {
		recs, err := Frames(data)
		if err != nil {
			return
		}
		total := 0
		for _, r := range recs {
			total += 4 + len(r)
		}
		if total != len(data) {
			t.Fatalf("records cover %d of %d bytes", total, len(data))
		}
		rebuilt := make([]byte, 0, len(data))
		for _, r := range recs {
			rebuilt = binary.BigEndian.AppendUint32(rebuilt, uint32(len(r))|0x80000000)
			rebuilt = append(rebuilt, r...)
		}
		if string(rebuilt) != string(data) {
			t.Fatal("re-framing does not reproduce the input")
		}
	})
}

// FuzzGzFrames checks that any gzip-compressed archive payload either
// decompresses and frames into records that exactly cover it, or fails
// cleanly; it never panics or over-reads. Seeded with the testdata checkpoint files.
func FuzzGzFrames(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff})

	// Seed with testdata checkpoint files
	_ = filepath.Walk("../../testdata/archive", func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".gz") {
			if data, err := os.ReadFile(path); err == nil {
				f.Add(data)
			}
		}
		return nil
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := gunzip(data)
		if err != nil {
			return
		}
		recs, err := Frames(raw)
		if err != nil {
			return
		}
		total := 0
		for _, r := range recs {
			total += 4 + len(r)
		}
		if total != len(raw) {
			t.Fatalf("records cover %d of %d bytes", total, len(raw))
		}
		rebuilt := make([]byte, 0, len(raw))
		for _, r := range recs {
			rebuilt = binary.BigEndian.AppendUint32(rebuilt, uint32(len(r))|0x80000000)
			rebuilt = append(rebuilt, r...)
		}
		if !bytes.Equal(rebuilt, raw) {
			t.Fatal("re-framing does not reproduce the decompressed input")
		}
	})
}
