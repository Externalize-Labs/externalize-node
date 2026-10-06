package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/bundle"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
	"github.com/Externalize-Labs/externalize-node/internal/server"
)

const (
	tx4  = "9689498be8097cf8b4e0465fff232b8bab486709afcf5aebffdd9ac51f91f873"
	tx25 = "764c39734ec4da0b537f8c5e43b20223274064f84705b18943b1c35512f8da48"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	arch := httptest.NewServer(http.FileServer(http.Dir("../../testdata/archive")))
	t.Cleanup(arch.Close)
	rpcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params map[string]string `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		meta, err := os.ReadFile("../../testdata/rpc/meta-" + req.Params["hash"][:8] + ".xdr.b64")
		result := map[string]any{"status": "NOT_FOUND"}
		if err == nil {
			result = map[string]any{"status": "SUCCESS", "ledger": 64791359, "resultMetaXdr": strings.TrimSpace(string(meta))}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
	t.Cleanup(rpcSrv.Close)

	s := &server.Server{
		Builder: &bundle.Builder{
			Network: "Public Global Stellar Network ; September 2015",
			Archive: archive.NewClient(arch.URL, ""),
			RPC:     rpc.NewClient(rpcSrv.URL),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

func TestLedgerBundleMatchesFixture(t *testing.T) {
	srv := newServer(t)
	code, body, hdr := get(t, srv, "/v1/ledgers/64791359/bundle?tx="+tx25+"&invocation="+tx4+":0")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	want, _ := os.ReadFile("../../testdata/bundle-64791359.json")
	if body != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatal("served bundle differs from the conformance fixture")
	}
	if !strings.Contains(hdr.Get("Cache-Control"), "immutable") {
		t.Fatal("bundles should be cacheable forever")
	}
}

func TestTransactionBundle(t *testing.T) {
	srv := newServer(t)
	code, body, _ := get(t, srv, "/v1/transactions/"+tx4+"/bundle?op=0")
	if code != http.StatusOK || !strings.Contains(body, `"kind": "invocation"`) {
		t.Fatalf("status %d: %s", code, body)
	}
}

func TestErrorsMapToStatusCodes(t *testing.T) {
	srv := newServer(t)
	for path, want := range map[string]int{
		"/v1/ledgers/0/bundle":                                          http.StatusBadRequest,
		"/v1/ledgers/abc/bundle":                                        http.StatusBadRequest,
		"/v1/ledgers/64791359/bundle?tx=XYZ":                            http.StatusBadRequest,
		"/v1/ledgers/64791359/bundle?invocation=" + tx4 + ":x":          http.StatusBadRequest,
		"/v1/ledgers/64791423/bundle":                                   http.StatusNotFound,
		"/v1/transactions/" + strings.Repeat("cd", 32) + "/bundle?op=0": http.StatusNotFound,
		"/v1/transactions/" + tx4 + "/bundle?op=-1":                     http.StatusBadRequest,
	} {
		if code, body, _ := get(t, srv, path); code != want {
			t.Errorf("%s: status %d, want %d (%s)", path, code, want, body)
		}
	}
}

func TestHealth(t *testing.T) {
	code, body, _ := get(t, newServer(t), "/healthz")
	if code != http.StatusOK || !strings.Contains(body, `"rpc":true`) {
		t.Fatalf("status %d: %s", code, body)
	}
}
