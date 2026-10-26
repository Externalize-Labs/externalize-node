// Package rpc is a minimal Stellar RPC client: just what bundles need.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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

// Source returns transactions by hash and the invocations behind contract events.
type Source interface {
	GetTransaction(ctx context.Context, hash string) (*Transaction, error)
	ContractInvocations(ctx context.Context, contract string, ledger uint32) ([]Invocation, error)
}

// Invocation is one contract-calling operation.
type Invocation struct {
	TxHash  string
	OpIndex uint32
}

// UserAgent is sent with every RPC request.
var UserAgent = "exnode"

// Client talks JSON-RPC to one or more Stellar RPC endpoints with failover.
type Client struct {
	Endpoints []string
	HTTP      *http.Client
	Attempts  int           // rounds over all endpoints
	Backoff   time.Duration // wait after the first failed round; doubles each round

	mu       sync.RWMutex
	lastUsed string
}

// NewClient returns a client for one or more comma-separated RPC URLs.
func NewClient(raw string) *Client {
	var urls []string
	for _, u := range strings.Split(raw, ",") {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			urls = append(urls, u)
		}
	}
	return &Client{
		Endpoints: urls,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Attempts:  3,
		Backoff:   500 * time.Millisecond,
	}
}

// LastEndpoint returns the URL of the most recently successful RPC endpoint.
func (c *Client) LastEndpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastUsed
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

// ContractInvocations lists, in ledger order and without duplicates, the
// operations that emitted events from contract in ledger, keeping only those
// inside successful contract calls.
func (c *Client) ContractInvocations(ctx context.Context, contract string, ledger uint32) ([]Invocation, error) {
	type event struct {
		Ledger         uint32 `json:"ledger"`
		TxHash         string `json:"txHash"`
		OperationIndex uint32 `json:"operationIndex"`
		InSuccessful   bool   `json:"inSuccessfulContractCall"`
	}
	params := map[string]any{
		"startLedger": ledger,
		"endLedger":   ledger + 1,
		"filters":     []any{map[string]any{"type": "contract", "contractIds": []string{contract}}},
		"pagination":  map[string]any{"limit": 1000},
	}
	seen := map[Invocation]bool{}
	var out []Invocation
	for page := 0; page < 100; page++ {
		var res struct {
			Events []event `json:"events"`
			Cursor string  `json:"cursor"`
		}
		if err := c.call(ctx, "getEvents", params, &res); err != nil {
			return nil, err
		}
		for _, e := range res.Events {
			inv := Invocation{TxHash: e.TxHash, OpIndex: e.OperationIndex}
			if e.Ledger == ledger && e.InSuccessful && !seen[inv] {
				seen[inv] = true
				out = append(out, inv)
			}
		}
		if len(res.Events) < 1000 || res.Cursor == "" {
			return out, nil
		}
		params = map[string]any{
			"filters":    params["filters"],
			"pagination": map[string]any{"cursor": res.Cursor, "limit": 1000},
		}
	}
	return nil, fmt.Errorf("getEvents: too many events for %s in ledger %d", contract, ledger)
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
	if len(c.Endpoints) == 0 {
		return errors.New("no rpc endpoints configured")
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}

	var failures []error
	wait := c.Backoff
	rounds := max(c.Attempts, 1)

	for round := 0; round < rounds; round++ {
		for i := range c.Endpoints {
			endpoint := c.Endpoints[(round+i)%len(c.Endpoints)]
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				failures = append(failures, err)
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", UserAgent)

			resp, err := c.HTTP.Do(req)
			if err != nil {
				slog.Debug("rpc endpoint unreachable, attempting failover", "endpoint", endpoint, "error", err)
				failures = append(failures, fmt.Errorf("%s: %w", endpoint, err))
				continue
			}
			if resp.StatusCode >= 500 {
				resp.Body.Close()
				slog.Debug("rpc endpoint returned 5xx, attempting failover", "endpoint", endpoint, "status", resp.StatusCode)
				failures = append(failures, fmt.Errorf("%s: HTTP %d", endpoint, resp.StatusCode))
				continue
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				return fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
			}

			var env struct {
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&env)
			resp.Body.Close()
			if decodeErr != nil {
				failures = append(failures, fmt.Errorf("%s: decoding response: %w", endpoint, decodeErr))
				continue
			}
			if env.Error != nil {
				return fmt.Errorf("%s: RPC error %d: %s", method, env.Error.Code, env.Error.Message)
			}

			c.mu.Lock()
			c.lastUsed = endpoint
			c.mu.Unlock()
			slog.Debug("rpc request succeeded", "endpoint", endpoint, "method", method)

			return json.Unmarshal(env.Result, result)
		}

		if round+1 < rounds {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
				wait *= 2
			}
		}
	}

	return fmt.Errorf("all rpc endpoints failed: %v", failures)
}
