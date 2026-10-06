package archive

import (
	"encoding/binary"
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
