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

func TestFromEnvRejectsUnknownNetwork(t *testing.T) {
	t.Setenv("EXNODE_NETWORK", "moonnet")
	if _, err := FromEnv(); err == nil {
		t.Fatal("unknown network accepted")
	}
}

func TestFromEnvConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, h http.Handler)
	}{
		{
			name: "EXNODE_CORS allows listed origins and rejects others",
			env: map[string]string{
				"EXNODE_CORS": "https://a.example, https://b.example",
			},
			check: func(t *testing.T, h http.Handler) {
				origins := []struct {
					origin  string
					allowed bool
				}{
					{"https://a.example", true},
					{"https://b.example", true},
					{"https://c.example", false},
					{"https://other.example", false},
				}
				for _, tc := range origins {
					req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
					req.Header.Set("Origin", tc.origin)
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)

					got := rec.Header().Get("Access-Control-Allow-Origin")
					if tc.allowed && got != tc.origin {
						t.Errorf("origin %q: want Access-Control-Allow-Origin %q, got %q", tc.origin, tc.origin, got)
					}
					if !tc.allowed && got != "" {
						t.Errorf("origin %q: want no Access-Control-Allow-Origin, got %q", tc.origin, got)
					}
				}
			},
		},
		{
			name: "EXNODE_NETWORK testnet reports testnet passphrase",
			env: map[string]string{
				"EXNODE_NETWORK": "testnet",
			},
			check: func(t *testing.T, h http.Handler) {
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				var body struct {
					Network string `json:"network"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				const want = "Test SDF Network ; September 2015"
				if body.Network != want {
					t.Errorf("network passphrase = %q, want %q", body.Network, want)
				}
			},
		},
		{
			name: "no EXNODE_RPC reports rpc false",
			env: map[string]string{
				"EXNODE_RPC": "",
			},
			check: func(t *testing.T, h http.Handler) {
				req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				var body struct {
					RPC bool `json:"rpc"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if body.RPC {
					t.Errorf("rpc = true, want false")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			h, err := FromEnv()
			if err != nil {
				t.Fatalf("FromEnv: %v", err)
			}
			tt.check(t, h)
		})
	}
}
