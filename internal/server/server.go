// Package server exposes proof bundles over HTTP.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/bundle"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
)

// Server serves bundles built by a Builder.
type Server struct {
	Builder *bundle.Builder
	Log     *slog.Logger
	// Optional sources for GET /v1/status.
	ArchiveTip func(context.Context) (uint32, error)
	RPCLatest  func(context.Context) (uint32, error)
	// RatePerSecond and RateBurst limit /v1 requests per client IP (0 disables).
	RatePerSecond float64
	RateBurst     float64
	// CORSOrigins lists browser origins allowed to call the API ("*" for any).
	CORSOrigins []string

	metrics *metrics
}

// Handler returns the HTTP routes.
//
//	GET /healthz
//	GET /metrics
//	GET /v1/status
//	GET /v1/ledgers/{seq}/bundle?tx=<hash>&invocation=<hash>:<op>&contract=<C…>&txset=true
//	GET /v1/transactions/{hash}/bundle?op=<n>
func (s *Server) Handler() http.Handler {
	s.metrics = newMetrics()
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", s.metrics)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/ledgers/{seq}/bundle", s.ledgerBundle)
	mux.HandleFunc("GET /v1/transactions/{hash}/bundle", s.transactionBundle)
	limited := withRateLimit(newRateLimiter(s.RatePerSecond, max(s.RateBurst, 1)), mux)
	return withAccessLog(s.Log, s.metrics, withCORS(s.CORSOrigins, limited))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "network": s.Builder.Network, "rpc": s.Builder.RPC != nil})
}

// status reports how far the archive trails the network: ledgers newer than
// the archive tip cannot be bundled yet.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out := map[string]any{"network": s.Builder.Network}
	var tip, latest uint32
	var tipErr, rpcErr error
	var wg sync.WaitGroup
	if s.ArchiveTip != nil {
		wg.Add(1)
		go func() { defer wg.Done(); tip, tipErr = s.ArchiveTip(ctx) }()
	}
	if s.RPCLatest != nil {
		wg.Add(1)
		go func() { defer wg.Done(); latest, rpcErr = s.RPCLatest(ctx) }()
	}
	wg.Wait()
	if s.ArchiveTip != nil {
		out["archive_tip"] = errOr(tip, tipErr)
	}
	if s.RPCLatest != nil {
		out["rpc_latest"] = errOr(latest, rpcErr)
	}
	if tipErr == nil && rpcErr == nil && tip > 0 && latest >= tip {
		out["archive_lag_ledgers"] = latest - tip
	}
	writeJSON(w, http.StatusOK, out)
}

func errOr(v uint32, err error) any {
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	return v
}

func (s *Server) ledgerBundle(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 32)
	if err != nil || seq == 0 {
		writeError(w, http.StatusBadRequest, "ledger must be a positive 32-bit integer")
		return
	}
	q := r.URL.Query()
	req := bundle.Request{Ledger: uint32(seq), Transactions: q["tx"], Contracts: q["contract"], WithTxSet: q.Get("txset") == "true"}
	for _, v := range q["invocation"] {
		inv, err := parseInvocation(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Invocations = append(req.Invocations, inv)
	}
	s.serve(w, r, req)
}

func (s *Server) transactionBundle(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	req := bundle.Request{}
	if op := r.URL.Query().Get("op"); op != "" {
		n, err := strconv.ParseUint(op, 10, 32)
		if err != nil {
			writeError(w, http.StatusBadRequest, "op must be a non-negative integer")
			return
		}
		req.Invocations = []bundle.Invocation{{TxHash: hash, OpIndex: uint32(n)}}
	} else {
		req.Transactions = []string{hash}
	}
	s.serve(w, r, req)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, req bundle.Request) {
	b, err := s.Builder.Build(r.Context(), req)
	if err != nil {
		s.metrics.failed.Add(1)
		status := statusFor(err)
		if status >= 500 {
			s.Log.Error("building bundle", "path", r.URL.Path, "err", err)
		}
		writeError(w, status, err.Error())
		return
	}
	var buf bytes.Buffer
	if err := bundle.Encode(&buf, b); err != nil {
		writeError(w, http.StatusInternalServerError, "encoding bundle")
		return
	}
	s.metrics.built.Add(1)
	w.Header().Set("Content-Type", "application/json")
	// Archived ledgers never change, so neither does a bundle built from one.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(buf.Bytes())
}

func parseInvocation(v string) (bundle.Invocation, error) {
	hash, op, ok := strings.Cut(v, ":")
	if !ok {
		return bundle.Invocation{TxHash: v}, nil
	}
	n, err := strconv.ParseUint(op, 10, 32)
	if err != nil {
		return bundle.Invocation{}, errors.New("invocation must be <tx hash> or <tx hash>:<op index>")
	}
	return bundle.Invocation{TxHash: hash, OpIndex: uint32(n)}, nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, bundle.ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, archive.ErrNotPublished), errors.Is(err, rpc.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, bundle.ErrNoRPC):
		return http.StatusNotImplemented
	default:
		return http.StatusBadGateway
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
