package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, reply func(method string) any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(reply(req.Method))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL)
}

func TestGetTransaction(t *testing.T) {
	c := fake(t, func(string) any {
		return map[string]any{"result": map[string]any{"status": "SUCCESS", "ledger": 7, "resultMetaXdr": "AAAA"}}
	})
	tx, err := c.GetTransaction(context.Background(), "ab")
	if err != nil || tx.Ledger != 7 || tx.ResultMetaXDR != "AAAA" {
		t.Fatalf("%+v %v", tx, err)
	}
}

func TestNotFoundIsDistinguishable(t *testing.T) {
	c := fake(t, func(string) any { return map[string]any{"result": map[string]any{"status": "NOT_FOUND"}} })
	if _, err := c.GetTransaction(context.Background(), "ab"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRPCErrorsAreReported(t *testing.T) {
	c := fake(t, func(string) any {
		return map[string]any{"error": map[string]any{"code": -32602, "message": "invalid hash"}}
	})
	_, err := c.GetTransaction(context.Background(), "zz")
	if err == nil || !strings.Contains(err.Error(), "invalid hash") {
		t.Fatalf("got %v", err)
	}
}

func TestLatestLedger(t *testing.T) {
	c := fake(t, func(m string) any {
		if m != "getLatestLedger" {
			t.Errorf("unexpected method %s", m)
		}
		return map[string]any{"result": map[string]any{"sequence": 64791999}}
	})
	if n, err := c.LatestLedger(context.Background()); err != nil || n != 64791999 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestRPCFailoverBetweenEndpoints(t *testing.T) {
	// First server returns 503
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
	}))
	defer srv1.Close()

	// Second server succeeds
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{
				"status":        "SUCCESS",
				"ledger":        1234,
				"resultMetaXdr": "BBBB",
			},
		})
	}))
	defer srv2.Close()

	c := NewClient(srv1.URL + ", " + srv2.URL)
	c.Backoff = time.Millisecond

	tx, err := c.GetTransaction(context.Background(), "failover_tx")
	if err != nil {
		t.Fatalf("expected successful failover to srv2, got err: %v", err)
	}
	if tx.Ledger != 1234 || tx.ResultMetaXDR != "BBBB" {
		t.Fatalf("unexpected tx data: %+v", tx)
	}
	if c.LastEndpoint() != srv2.URL {
		t.Fatalf("expected lastUsed endpoint to be %s, got %s", srv2.URL, c.LastEndpoint())
	}
}
