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
