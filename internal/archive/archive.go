// Package archive reads Stellar history archives: checkpoint layout, framed
// XDR streams, and an on-disk cache of immutable checkpoint files.
package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CheckpointFrequency is the number of ledgers per archive checkpoint.
const CheckpointFrequency = 64

// maxFileSize bounds a decompressed checkpoint file. Mainnet transaction
// files are tens of megabytes; anything past this is treated as hostile.
const maxFileSize = 512 << 20

// Category is a history archive file category.
type Category string

// Categories used to build proof bundles.
const (
	Headers      Category = "ledger"
	SCP          Category = "scp"
	Results      Category = "results"
	Transactions Category = "transactions"
)

// Checkpoint returns the checkpoint ledger whose files contain seq.
func Checkpoint(seq uint32) uint32 {
	return (seq/CheckpointFrequency+1)*CheckpointFrequency - 1
}

// Path returns the archive-relative path of a checkpoint file, e.g.
// "scp/03/dc/a3/scp-03dca33f.xdr.gz".
func Path(c Category, checkpoint uint32) string {
	h := fmt.Sprintf("%08x", checkpoint)
	return fmt.Sprintf("%s/%s/%s/%s/%s-%s.xdr.gz", c, h[0:2], h[2:4], h[4:6], c, h)
}

// UserAgent is sent with every archive request.
var UserAgent = "exnode"

// Client fetches checkpoint files over HTTP and caches them on disk.
// Checkpoint files never change once published, so the cache never expires.
//
// With several mirrors, each attempt walks the mirrors in turn: a mirror that
// errors, times out, 404s or serves a corrupt file is skipped, and a full
// round of failures backs off exponentially before the next attempt.
type Client struct {
	Mirrors  []string
	CacheDir string // empty disables caching
	HTTP     *http.Client
	Attempts int           // rounds over all mirrors
	Backoff  time.Duration // wait after the first failed round; doubles each round

	memory *lru
}

// NewClient returns a client for one or more comma-separated mirror URLs.
func NewClient(mirrors, cacheDir string) *Client {
	var urls []string
	for _, m := range strings.Split(mirrors, ",") {
		if m = strings.TrimRight(strings.TrimSpace(m), "/"); m != "" {
			urls = append(urls, m)
		}
	}
	return &Client{
		Mirrors:  urls,
		CacheDir: cacheDir,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
		Attempts: 3,
		Backoff:  500 * time.Millisecond,
		memory:   newLRU(16),
	}
}

// SetMemoryFiles sets how many decoded checkpoint files are kept in memory (0 disables).
func (c *Client) SetMemoryFiles(n int) { c.memory = newLRU(n) }

// File returns the decompressed contents of a checkpoint file.
func (c *Client) File(ctx context.Context, cat Category, checkpoint uint32) ([]byte, error) {
	rel := Path(cat, checkpoint)
	if c.CacheDir != "" {
		if gz, err := os.ReadFile(filepath.Join(c.CacheDir, filepath.FromSlash(rel))); err == nil {
			return gunzip(gz)
		}
	}
	gz, raw, err := c.fetch(ctx, rel)
	if err != nil {
		return nil, err
	}
	if c.CacheDir != "" {
		_ = writeAtomic(filepath.Join(c.CacheDir, filepath.FromSlash(rel)), gz)
	}
	return raw, nil
}

// fetch tries every mirror, up to Attempts rounds, and returns the gzip and
// its decompressed contents from the first mirror that serves a valid file.
func (c *Client) fetch(ctx context.Context, rel string) ([]byte, []byte, error) {
	if len(c.Mirrors) == 0 {
		return nil, nil, errors.New("no archive mirrors configured")
	}
	var failures []error
	wait := c.Backoff
	rounds := max(c.Attempts, 1)
	for round := 0; round < rounds; round++ {
		missing := 0
		for i := range c.Mirrors {
			mirror := c.Mirrors[(round+i)%len(c.Mirrors)]
			gz, err := c.download(ctx, mirror, rel)
			if err == nil {
				raw, gzErr := gunzip(gz)
				if gzErr == nil {
					return gz, raw, nil
				}
				err = fmt.Errorf("%s/%s: corrupt file: %w", mirror, rel, gzErr)
			}
			if errors.Is(err, ErrNotPublished) {
				missing++
				// One mirror's 404 is not proof of absence; keep it out of the error chain.
				err = fmt.Errorf("%s: not found", mirror)
			}
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			failures = append(failures, err)
		}
		if missing == len(c.Mirrors) {
			return nil, nil, fmt.Errorf("%s: %w", rel, ErrNotPublished)
		}
		if round+1 < rounds {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
	}
	return nil, nil, fmt.Errorf("fetching %s: %w", rel, errors.Join(failures...))
}

func (c *Client) download(ctx context.Context, mirror, rel string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mirror+"/"+rel, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", rel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", rel, ErrNotPublished)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: HTTP %d", rel, resp.StatusCode)
	}
	return readLimited(resp.Body)
}

// Tip returns the latest checkpoint ledger an archive mirror has published.
func (c *Client) Tip(ctx context.Context) (uint32, error) {
	var failures []error
	for _, m := range c.Mirrors {
		raw, err := c.download(ctx, m, ".well-known/stellar-history.json")
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var has struct {
			CurrentLedger uint32 `json:"currentLedger"`
		}
		if err := json.Unmarshal(raw, &has); err != nil || has.CurrentLedger == 0 {
			failures = append(failures, fmt.Errorf("%s: unreadable stellar-history.json", m))
			continue
		}
		return has.CurrentLedger, nil
	}
	return 0, fmt.Errorf("archive tip: %w", errors.Join(failures...))
}

// ErrNotPublished means the archive has no file for the checkpoint yet.
var ErrNotPublished = errors.New("checkpoint not published")

func gunzip(gz []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readLimited(r)
}

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileSize {
		return nil, errors.New("file exceeds size limit")
	}
	return b, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Frames splits an RFC 5531 record-marked stream into its records.
// Archive files contain one single-fragment record per entry.
func Frames(data []byte) ([][]byte, error) {
	var out [][]byte
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, errors.New("truncated record mark")
		}
		mark := binary.BigEndian.Uint32(data)
		if mark&0x80000000 == 0 {
			return nil, errors.New("multi-fragment records are not supported")
		}
		n := int(mark & 0x7fffffff)
		data = data[4:]
		if n > len(data) {
			return nil, errors.New("truncated record")
		}
		out = append(out, data[:n])
		data = data[n:]
	}
	return out, nil
}
