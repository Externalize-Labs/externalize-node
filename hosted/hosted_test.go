package hosted

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("EXNODE_RPC", "https://rpc.example")
	h, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://externalize-labs.github.io")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		Network string `json:"network"`
		RPC     bool   `json:"rpc"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body)
	}
	if body.Network != "Public Global Stellar Network ; September 2015" || !body.RPC {
		t.Fatalf("health %+v", body)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("a hosted node answers browsers on any origin by default")
	}
}

func TestFromEnvServesV2Bundles(t *testing.T) {
	arch := httptest.NewServer(http.FileServer(http.Dir("../testdata/archive")))
	defer arch.Close()
	t.Setenv("EXNODE_ARCHIVE", arch.URL)
	t.Setenv("EXNODE_CACHE", t.TempDir())
	h, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ledgers/64791359/bundle", nil))
	var b map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %.200q", rec.Code, rec.Body)
	}
	if b["format"] != "externalize/bundle/v2" || b["next_scp"] == nil || b["next_transactions"] == nil {
		t.Fatalf("not a v2 bundle: format %v", b["format"])
	}
}

func TestFromEnvRejectsUnknownNetwork(t *testing.T) {
	t.Setenv("EXNODE_NETWORK", "moonnet")
	if _, err := FromEnv(); err == nil {
		t.Fatal("unknown network accepted")
	}
}
