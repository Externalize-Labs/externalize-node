package bundle_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/bundle"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const (
	public   = "Public Global Stellar Network ; September 2015"
	ledger   = 64791359
	tx4      = "9689498be8097cf8b4e0465fff232b8bab486709afcf5aebffdd9ac51f91f873"
	tx25     = "764c39734ec4da0b537f8c5e43b20223274064f84705b18943b1c35512f8da48"
	testdata = "../../testdata"
)

// newBuilder serves testdata through the real archive and RPC clients.
func newBuilder(t *testing.T, withRPC bool) *bundle.Builder {
	t.Helper()
	arch := httptest.NewServer(http.FileServer(http.Dir(testdata + "/archive")))
	t.Cleanup(arch.Close)
	b := &bundle.Builder{Network: public, Archive: archive.NewClient(arch.URL, "")}
	if withRPC {
		srv := httptest.NewServer(http.HandlerFunc(fakeRPC(t)))
		t.Cleanup(srv.Close)
		b.RPC = rpc.NewClient(srv.URL)
	}
	return b
}

func fakeRPC(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method == "getEvents" {
			// The pool contract emitted two events in tx4's first operation.
			ev := func(i int) map[string]any {
				return map[string]any{"ledger": ledger, "txHash": tx4, "operationIndex": 0, "inSuccessfulContractCall": true, "id": i}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"events": []any{ev(1), ev(2)}}})
			return
		}
		hash, _ := req.Params["hash"].(string)
		meta, err := os.ReadFile(testdata + "/rpc/meta-" + hash[:8] + ".xdr.b64")
		result := map[string]any{"status": "NOT_FOUND"}
		if err == nil {
			result = map[string]any{"status": "SUCCESS", "ledger": ledger, "resultMetaXdr": strings.TrimSpace(string(meta))}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}
}

func encode(t *testing.T, b *bundle.Bundle) string {
	t.Helper()
	var buf bytes.Buffer
	if err := bundle.Encode(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func golden(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(testdata + "/bundle-64791359.json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// The bundle externalize-core verifies in its own test suite. Byte equality
// here means the Go builder and the Rust verifier agree on the wire format.
func TestMatchesRustConformanceFixture(t *testing.T) {
	b, err := newBuilder(t, true).Build(context.Background(), bundle.Request{
		Ledger:       ledger,
		Transactions: []string{tx25},
		Invocations:  []bundle.Invocation{{TxHash: tx4, OpIndex: 0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := encode(t, b), golden(t); got != want {
		t.Fatalf("bundle differs from Rust fixture (got %d bytes, want %d)", len(got), len(want))
	}
}

func TestLedgerIsResolvedThroughRPC(t *testing.T) {
	b, err := newBuilder(t, true).Build(context.Background(), bundle.Request{
		Transactions: []string{tx25},
		Invocations:  []bundle.Invocation{{TxHash: tx4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if encode(t, b) != golden(t) {
		t.Fatal("bundle built without an explicit ledger differs from the fixture")
	}
}

func TestPlainTransactionClaimsNeedNoRPC(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger, Transactions: []string{tx25}})
	if err != nil {
		t.Fatal(err)
	}
	if b.Results == "" || len(b.Claims) != 1 {
		t.Fatalf("expected results and one claim, got %+v", b.Claims)
	}
}

func TestCertificateOnlyBundleOmitsResults(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger})
	if err != nil {
		t.Fatal(err)
	}
	if b.Results != "" || b.Claims != nil {
		t.Fatal("a bundle with no claims should carry only the certificate")
	}
	if strings.Contains(encode(t, b), "claims") {
		t.Fatal("empty claims must be omitted, as externalize-core does")
	}
}

func TestTransactionSetIsIncludedOnRequest(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger, Transactions: []string{tx25}, WithTxSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.Transactions == "" {
		t.Fatal("transaction set missing")
	}
}

func TestRequestValidation(t *testing.T) {
	cases := map[string]bundle.Request{
		"empty":     {},
		"short":     {Ledger: ledger, Transactions: []string{"abcd"}},
		"uppercase": {Ledger: ledger, Transactions: []string{strings.ToUpper(tx25)}},
		"too many":  {Ledger: ledger, Transactions: make([]string, bundle.MaxClaims+1)},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := newBuilder(t, false).Build(context.Background(), req)
			if !errors.Is(err, bundle.ErrBadRequest) {
				t.Fatalf("want ErrBadRequest, got %v", err)
			}
		})
	}
}

func TestInvocationsRequireRPC(t *testing.T) {
	_, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger, Invocations: []bundle.Invocation{{TxHash: tx4}}})
	if !errors.Is(err, bundle.ErrNoRPC) {
		t.Fatalf("want ErrNoRPC, got %v", err)
	}
}

func TestUnknownTransactionSurfacesRPCNotFound(t *testing.T) {
	_, err := newBuilder(t, true).Build(context.Background(), bundle.Request{Invocations: []bundle.Invocation{{TxHash: strings.Repeat("ab", 32)}}})
	if !errors.Is(err, rpc.ErrNotFound) {
		t.Fatalf("want rpc.ErrNotFound, got %v", err)
	}
}

func TestInvocationOperationOutOfRange(t *testing.T) {
	_, err := newBuilder(t, true).Build(context.Background(), bundle.Request{Invocations: []bundle.Invocation{{TxHash: tx4, OpIndex: 3}}})
	if !errors.Is(err, bundle.ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
}

func TestLedgerMissingFromArchive(t *testing.T) {
	_, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger + 64})
	if !errors.Is(err, archive.ErrNotPublished) || errors.Is(err, archive.ErrSuccessorNotPublished) {
		t.Fatalf("want ErrNotPublished for the ledger itself, got %v", err)
	}
}

// requireSuccessorBindsHeader checks, as the verifier does, that the bundle's
// successor records commit to its header: some EXTERNALIZE for ledger N+1
// carries a StellarValue whose txSetHash is the hash of next_transactions'
// generalized set, and that set's previousLedgerHash is the hash of header N.
func requireSuccessorBindsHeader(t *testing.T, b *bundle.Bundle) {
	t.Helper()
	var head xdr.LedgerHeaderHistoryEntry
	var scp xdr.ScpHistoryEntry
	var next xdr.TransactionHistoryEntry
	decode := func(field, s string, v any) {
		t.Helper()
		if s == "" {
			t.Fatalf("%s missing", field)
		}
		if err := xdr.SafeUnmarshalBase64(s, v); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
	}
	decode("ledger", b.Ledger, &head)
	decode("next_scp", b.NextSCP, &scp)
	decode("next_transactions", b.NextTransactions, &next)
	raw, err := head.Header.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(raw) != [32]byte(head.Hash) {
		t.Fatal("ledger hash does not match its header")
	}
	seq := uint32(head.Header.LedgerSeq) + 1
	if uint32(next.LedgerSeq) != seq || scp.V0 == nil || uint32(scp.V0.LedgerMessages.LedgerSeq) != seq {
		t.Fatalf("successor records are not for ledger %d", seq)
	}
	if next.Ext.V != 1 || next.Ext.GeneralizedTxSet == nil || next.Ext.GeneralizedTxSet.V1TxSet == nil {
		t.Fatal("next_transactions carries no generalized transaction set")
	}
	if next.Ext.GeneralizedTxSet.V1TxSet.PreviousLedgerHash != head.Hash {
		t.Fatal("next transaction set does not point back at this header")
	}
	set, err := next.Ext.GeneralizedTxSet.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	setHash := sha256.Sum256(set)
	externalized := 0
	for _, env := range scp.V0.LedgerMessages.Messages {
		ext := env.Statement.Pledges.Externalize
		if ext == nil || uint64(env.Statement.SlotIndex) != uint64(seq) {
			continue
		}
		var v xdr.StellarValue
		if err := xdr.SafeUnmarshal(ext.Commit.Value, &v); err != nil {
			t.Fatal(err)
		}
		if [32]byte(v.TxSetHash) != setHash {
			t.Fatal("an EXTERNALIZE commits to a different transaction set")
		}
		externalized++
	}
	if externalized == 0 {
		t.Fatal("next_scp has no EXTERNALIZE for the successor")
	}
}

// 64791359 ends its checkpoint: ledger 64791360's records come from the
// checkpoint 64791423 files.
func TestSuccessorAtACheckpointBoundary(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger})
	if err != nil {
		t.Fatal(err)
	}
	requireSuccessorBindsHeader(t, b)
}

// 64791358's successor is in the same checkpoint's files.
func TestSuccessorMidCheckpoint(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger - 1})
	if err != nil {
		t.Fatal(err)
	}
	requireSuccessorBindsHeader(t, b)
	if b.Results != "" || b.Transactions != "" {
		t.Fatal("a certificate-only bundle carries no results or transaction set of its own")
	}
}

func TestSuccessorNotPublishedYet(t *testing.T) {
	files := http.FileServer(http.Dir(testdata + "/archive"))
	arch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "03dca37f") { // the next checkpoint
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	}))
	defer arch.Close()
	b := &bundle.Builder{Network: public, Archive: archive.NewClient(arch.URL, "")}
	got, err := b.Build(context.Background(), bundle.Request{Ledger: ledger, Transactions: []string{tx25}})
	if !errors.Is(err, archive.ErrSuccessorNotPublished) || got != nil {
		t.Fatalf("want ErrSuccessorNotPublished and no bundle, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry in a few minutes") {
		t.Fatalf("error should tell the caller to retry: %v", err)
	}
	// The ledger before it needs nothing from the next checkpoint.
	if _, err := b.Build(context.Background(), bundle.Request{Ledger: ledger - 1}); err != nil {
		t.Fatal(err)
	}
}

func TestBundlesAreV2(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger})
	if err != nil {
		t.Fatal(err)
	}
	got := encode(t, b)
	if !strings.Contains(got, `"format": "externalize/bundle/v2"`) || !strings.Contains(got, `"next_scp": "`) || !strings.Contains(got, `"next_transactions": "`) {
		t.Fatalf("not a v2 bundle:\n%.300s", got)
	}
}

func TestFailedTransactionsCannotProveInvocations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"status": "FAILED", "ledger": ledger, "resultMetaXdr": ""}})
	}))
	defer srv.Close()
	b := newBuilder(t, false)
	b.RPC = rpc.NewClient(srv.URL)
	_, err := b.Build(context.Background(), bundle.Request{Invocations: []bundle.Invocation{{TxHash: tx4}}})
	if !errors.Is(err, bundle.ErrBadRequest) || !strings.Contains(err.Error(), "did not succeed") {
		t.Fatalf("want a did-not-succeed ErrBadRequest, got %v", err)
	}
}

const pool = "CCNXGPE4AQCSNEBZO3XJDKKDI3CRLYMVS6UWBBTVDLALLWMJEXBORQ2A"

func TestContractQueriesProveEveryInvocation(t *testing.T) {
	b, err := newBuilder(t, true).Build(context.Background(), bundle.Request{Ledger: ledger, Contracts: []string{pool}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Claims) != 1 || b.Claims[0].Kind != "invocation" || b.Claims[0].TxHash != tx4 || len(b.Claims[0].Events) != 4 {
		t.Fatalf("want one deduplicated invocation claim for tx4, got %+v", b.Claims)
	}
	// Identical to the conformance fixture's invocation claim.
	want := golden(t)
	got := encode(t, b)
	claim := got[strings.Index(got, `"kind": "invocation"`):]
	if !strings.Contains(want, claim[:strings.Index(claim, "]")]) {
		t.Fatal("contract-derived claim differs from the fixture's")
	}
}

func TestContractQueriesNeedALedgerAndAValidAddress(t *testing.T) {
	for name, req := range map[string]bundle.Request{
		"no ledger": {Transactions: []string{tx25}, Contracts: []string{pool}},
		"bad id":    {Ledger: ledger, Contracts: []string{"GABC"}},
	} {
		if _, err := newBuilder(t, true).Build(context.Background(), req); !errors.Is(err, bundle.ErrBadRequest) {
			t.Errorf("%s: want ErrBadRequest, got %v", name, err)
		}
	}
}

func TestDuplicateTransactionClaimsCollapse(t *testing.T) {
	b, err := newBuilder(t, false).Build(context.Background(), bundle.Request{Ledger: ledger, Transactions: []string{tx25, tx25, tx25}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Claims) != 1 {
		t.Fatalf("want 1 claim, got %d", len(b.Claims))
	}
}

func TestTestnetCertificates(t *testing.T) {
	arch := httptest.NewServer(http.FileServer(http.Dir(testdata + "/archive")))
	defer arch.Close()
	b := &bundle.Builder{Network: "Test SDF Network ; September 2015", Archive: archive.NewClient(arch.URL, "")}
	got, err := b.Build(context.Background(), bundle.Request{Ledger: 5052927})
	if err != nil {
		t.Fatal(err)
	}
	if got.Network != "Test SDF Network ; September 2015" || got.Ledger == "" || got.SCP == "" || got.Claims != nil {
		t.Fatalf("unexpected testnet bundle: %+v", got)
	}
	requireSuccessorBindsHeader(t, got)
}
