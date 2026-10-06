// Package rpc is a minimal Stellar RPC client: just what bundles need.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Transaction is the subset of getTransaction a bundle needs.
type Transaction struct {
	Status        string `json:"status"`
	Ledger        uint32 `json:"ledger"`
	ResultMetaXDR string `json:"resultMetaXdr"`
}

// ErrNotFound means the RPC does not know the transaction, either because it
// never existed or because it is older than the RPC's retention window.
var ErrNotFound = errors.New("transaction not found or outside RPC retention")

// Source returns transactions by hash.
type Source interface {
	GetTransaction(ctx context.Context, hash string) (*Transaction, error)
}

// UserAgent is sent with every RPC request.
var UserAgent = "exnode"

// Client talks JSON-RPC to a Stellar RPC endpoint.
type Client struct {
	URL  string
	HTTP *http.Client
}

// NewClient returns a client with a conservative timeout.
func NewClient(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// GetTransaction implements Source.
func (c *Client) GetTransaction(ctx context.Context, hash string) (*Transaction, error) {
	var out Transaction
	if err := c.call(ctx, "getTransaction", map[string]string{"hash": hash, "xdrFormat": "base64"}, &out); err != nil {
		return nil, err
	}
	if out.Status == "NOT_FOUND" {
		return nil, ErrNotFound
	}
	return &out, nil
}

// LatestLedger returns the newest ledger the RPC has ingested.
func (c *Client) LatestLedger(ctx context.Context) (uint32, error) {
	var out struct {
		Sequence uint32 `json:"sequence"`
	}
	if err := c.call(ctx, "getLatestLedger", struct{}{}, &out); err != nil {
		return 0, err
	}
	return out.Sequence, nil
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&env); err != nil {
		return fmt.Errorf("%s: decoding response: %w", method, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%s: RPC error %d: %s", method, env.Error.Code, env.Error.Message)
	}
	return json.Unmarshal(env.Result, result)
}
