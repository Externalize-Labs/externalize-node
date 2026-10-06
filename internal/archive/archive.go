// Package archive reads Stellar history archives: checkpoint layout, framed
// XDR streams, and an on-disk cache of immutable checkpoint files.
package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
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
type Client struct {
	BaseURL  string
	CacheDir string // empty disables caching
	HTTP     *http.Client
}

// NewClient returns a client with a conservative HTTP timeout.
func NewClient(baseURL, cacheDir string) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		CacheDir: cacheDir,
		HTTP:     &http.Client{Timeout: 2 * time.Minute},
	}
}

// File returns the decompressed contents of a checkpoint file.
func (c *Client) File(ctx context.Context, cat Category, checkpoint uint32) ([]byte, error) {
	rel := Path(cat, checkpoint)
	if c.CacheDir != "" {
		if gz, err := os.ReadFile(filepath.Join(c.CacheDir, filepath.FromSlash(rel))); err == nil {
			return gunzip(gz)
		}
	}
	gz, err := c.download(ctx, rel)
	if err != nil {
		return nil, err
	}
	raw, err := gunzip(gz)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if c.CacheDir != "" {
		_ = writeAtomic(filepath.Join(c.CacheDir, filepath.FromSlash(rel)), gz)
	}
	return raw, nil
}

func (c *Client) download(ctx context.Context, rel string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+rel, nil)
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
