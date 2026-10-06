// Package bundle assembles Externalize proof bundles.
//
// The node that builds a bundle is untrusted by design: verifiers check every
// byte against validator signatures. This package therefore never interprets
// what it ships beyond locating records; it copies archive XDR verbatim.
package bundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Format is the bundle format tag, shared with externalize-core.
const Format = "externalize/bundle/v1"

// MaxClaims bounds the claims in one bundle.
const MaxClaims = 64

// Bundle mirrors externalize-core's Bundle. Field order is the JSON order.
type Bundle struct {
	Format       string  `json:"format"`
	Network      string  `json:"network"`
	Ledger       string  `json:"ledger"`
	SCP          string  `json:"scp"`
	Results      string  `json:"results,omitempty"`
	Transactions string  `json:"transactions,omitempty"`
	Claims       []Claim `json:"claims,omitempty"`
}

// Claim is a transaction or an invocation claim.
type Claim struct {
	Kind        string   `json:"kind"`
	TxHash      string   `json:"tx_hash"`
	OpIndex     *uint32  `json:"op_index,omitempty"`
	ReturnValue string   `json:"return_value,omitempty"`
	Events      []string `json:"events,omitempty"`
}

// MarshalJSON keeps `events` present (possibly empty) on invocation claims.
func (c Claim) MarshalJSON() ([]byte, error) {
	if c.Kind != "invocation" {
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			TxHash string `json:"tx_hash"`
		}{c.Kind, c.TxHash})
	}
	events := c.Events
	if events == nil {
		events = []string{}
	}
	return json.Marshal(struct {
		Kind        string   `json:"kind"`
		TxHash      string   `json:"tx_hash"`
		OpIndex     *uint32  `json:"op_index"`
		ReturnValue string   `json:"return_value"`
		Events      []string `json:"events"`
	}{c.Kind, c.TxHash, c.OpIndex, c.ReturnValue, events})
}

// Invocation names one InvokeHostFunction operation.
type Invocation struct {
	TxHash  string
	OpIndex uint32
}

// Request describes the bundle to build. Ledger may be zero when at least one
// transaction is named; it is then taken from RPC.
//
// Contracts adds an invocation claim for every successful operation in Ledger
// that emitted an event from one of these contracts. Operations whose events
// come from classic operations (Stellar Asset Contract events since protocol
// 23) are skipped: they are not committed to the ledger and cannot be proven.
type Request struct {
	Ledger       uint32
	Transactions []string
	Invocations  []Invocation
	Contracts    []string
	WithTxSet    bool
}

// Builder turns requests into bundles.
type Builder struct {
	Network string
	Archive archive.Source
	RPC     rpc.Source // optional; required for invocations or when Ledger is zero
}

// Errors callers map to client-side failures.
var (
	ErrBadRequest = errors.New("bad request")
	ErrNoRPC      = errors.New("an RPC endpoint is required for this request")
)

// Build fetches everything the request needs and assembles the bundle.
func (b *Builder) Build(ctx context.Context, req Request) (*Bundle, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	needRPC := len(req.Invocations) > 0 || len(req.Contracts) > 0 || req.Ledger == 0
	if needRPC && b.RPC == nil {
		return nil, ErrNoRPC
	}
	skip := map[string]bool{} // contract-derived invocations that turned out not to be provable
	if len(req.Contracts) > 0 {
		derived, err := b.contractInvocations(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, inv := range derived {
			skip[inv.TxHash] = true
		}
		req.Invocations = append(req.Invocations, derived...)
		if n := len(req.Transactions) + len(req.Invocations); n > MaxClaims {
			return nil, fmt.Errorf("%w: %d claims exceeds the limit of %d", ErrBadRequest, n, MaxClaims)
		}
	}

	metas := map[string]*rpc.Transaction{}
	for _, h := range hashesNeedingRPC(req) {
		tx, err := b.RPC.GetTransaction(ctx, h)
		if err != nil {
			return nil, fmt.Errorf("transaction %s: %w", h, err)
		}
		if req.Ledger == 0 {
			req.Ledger = tx.Ledger
		}
		if tx.Status != "SUCCESS" && isInvocation(req, h) {
			return nil, fmt.Errorf("%w: transaction %s did not succeed (%s); failed transactions emit no provable events", ErrBadRequest, h, tx.Status)
		}
		if tx.Ledger != req.Ledger {
			return nil, fmt.Errorf("%w: transaction %s is in ledger %d, not %d", ErrBadRequest, h, tx.Ledger, req.Ledger)
		}
		metas[h] = tx
	}

	led, err := b.Archive.Ledger(ctx, req.Ledger, req.WithTxSet)
	if err != nil {
		return nil, err
	}
	out := &Bundle{
		Format:  Format,
		Network: b.Network,
		Ledger:  b64(led.Header),
		SCP:     b64(led.SCP),
	}
	hasClaims := len(req.Transactions)+len(req.Invocations) > 0
	if hasClaims {
		if led.Results == nil {
			return nil, fmt.Errorf("ledger %d has no results in the archive", req.Ledger)
		}
		out.Results = b64(led.Results)
	}
	if req.WithTxSet && led.Transactions != nil {
		out.Transactions = b64(led.Transactions)
	}

	for _, h := range req.Transactions {
		out.Claims = append(out.Claims, Claim{Kind: "transaction", TxHash: h})
	}
	for _, inv := range req.Invocations {
		ret, events, err := InvocationFromMeta(metas[inv.TxHash].ResultMetaXDR, inv.OpIndex)
		if err != nil && skip[inv.TxHash] && errors.Is(err, ErrBadRequest) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("transaction %s: %w", inv.TxHash, err)
		}
		op := inv.OpIndex
		out.Claims = append(out.Claims, Claim{Kind: "invocation", TxHash: inv.TxHash, OpIndex: &op, ReturnValue: ret, Events: events})
	}
	return out, nil
}

func (b *Builder) contractInvocations(ctx context.Context, req Request) ([]Invocation, error) {
	if req.Ledger == 0 {
		return nil, fmt.Errorf("%w: contract queries need a ledger", ErrBadRequest)
	}
	var out []Invocation
	seen := map[Invocation]bool{}
	for _, inv := range req.Invocations {
		seen[inv] = true
	}
	for _, c := range req.Contracts {
		found, err := b.RPC.ContractInvocations(ctx, c, req.Ledger)
		if err != nil {
			return nil, fmt.Errorf("contract %s: %w", c, err)
		}
		for _, f := range found {
			inv := Invocation{TxHash: f.TxHash, OpIndex: f.OpIndex}
			if !seen[inv] {
				seen[inv] = true
				out = append(out, inv)
			}
		}
	}
	return out, nil
}

func validate(req Request) error {
	if n := len(req.Transactions) + len(req.Invocations); n > MaxClaims {
		return fmt.Errorf("%w: %d claims exceeds the limit of %d", ErrBadRequest, n, MaxClaims)
	}
	if req.Ledger == 0 && len(req.Transactions)+len(req.Invocations) == 0 {
		return fmt.Errorf("%w: name a ledger or at least one transaction", ErrBadRequest)
	}
	for _, c := range req.Contracts {
		if len(c) != 56 || c[0] != 'C' {
			return fmt.Errorf("%w: %q is not a contract address", ErrBadRequest, c)
		}
	}
	check := func(h string) error {
		if b, err := hex.DecodeString(h); err != nil || len(b) != 32 || h != hex.EncodeToString(b) {
			return fmt.Errorf("%w: %q is not a lowercase 32-byte hex hash", ErrBadRequest, h)
		}
		return nil
	}
	for _, h := range req.Transactions {
		if err := check(h); err != nil {
			return err
		}
	}
	for _, inv := range req.Invocations {
		if err := check(inv.TxHash); err != nil {
			return err
		}
	}
	return nil
}

func isInvocation(req Request, hash string) bool {
	for _, inv := range req.Invocations {
		if inv.TxHash == hash {
			return true
		}
	}
	return false
}

// Only invocations need meta; plain transaction claims need RPC only to find the ledger.
func hashesNeedingRPC(req Request) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, inv := range req.Invocations {
		add(inv.TxHash)
	}
	if req.Ledger == 0 && len(out) == 0 && len(req.Transactions) > 0 {
		add(req.Transactions[0])
	}
	return out
}

// InvocationFromMeta extracts the return value and contract events of one
// InvokeHostFunction operation from base64 TransactionMeta (v3 or v4), each
// re-encoded as base64 XDR.
func InvocationFromMeta(metaB64 string, op uint32) (string, []string, error) {
	var meta xdr.TransactionMeta
	if err := xdr.SafeUnmarshalBase64(metaB64, &meta); err != nil {
		return "", nil, fmt.Errorf("decoding meta: %w", err)
	}
	var ret xdr.ScVal
	var events []xdr.ContractEvent
	switch {
	case meta.V4 != nil:
		if meta.V4.SorobanMeta == nil {
			return "", nil, fmt.Errorf("%w: not a Soroban invocation; events of classic operations are not committed to the ledger and cannot be proven", ErrBadRequest)
		}
		if int(op) >= len(meta.V4.Operations) {
			return "", nil, fmt.Errorf("%w: operation %d does not exist", ErrBadRequest, op)
		}
		events = meta.V4.Operations[op].Events
		ret = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
		if meta.V4.SorobanMeta.ReturnValue != nil {
			ret = *meta.V4.SorobanMeta.ReturnValue
		}
	case meta.V3 != nil && meta.V3.SorobanMeta != nil:
		if op != 0 {
			return "", nil, fmt.Errorf("%w: Soroban transactions have a single operation", ErrBadRequest)
		}
		events, ret = meta.V3.SorobanMeta.Events, meta.V3.SorobanMeta.ReturnValue
	default:
		return "", nil, fmt.Errorf("%w: transaction is not a Soroban invocation", ErrBadRequest)
	}
	retB64, err := xdr.MarshalBase64(ret)
	if err != nil {
		return "", nil, err
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		s, err := xdr.MarshalBase64(e)
		if err != nil {
			return "", nil, err
		}
		out = append(out, s)
	}
	return retB64, out, nil
}

// Encode writes the bundle as indented JSON with a trailing newline, the same
// bytes externalize-core produces.
func Encode(w io.Writer, b *Bundle) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
