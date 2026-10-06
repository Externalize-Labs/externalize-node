package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
