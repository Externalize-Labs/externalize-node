// Package api runs exnode as a Vercel serverless function: the same HTTP
// server as `exnode serve`, configured from the same EXNODE_* environment.
//
// vercel.json routes every path here, so a deployment answers the routes in
// openapi.yaml. Being untrusted by design, a hosted node needs no secrets:
// whatever it serves is checked against validator signatures by the client.
package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/bundle"
	"github.com/Externalize-Labs/externalize-node/internal/network"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
	"github.com/Externalize-Labs/externalize-node/internal/server"
)

var (
	once    sync.Once
	handler http.Handler
	initErr error
)

// Handler serves every exnode route. The server is built on the first
// request and reused while the function instance stays warm.
func Handler(w http.ResponseWriter, r *http.Request) {
	once.Do(func() { handler, initErr = fromEnv() })
	if initErr != nil {
		http.Error(w, "exnode is misconfigured: "+initErr.Error(), http.StatusInternalServerError)
		return
	}
	handler.ServeHTTP(w, r)
}

// fromEnv builds the server. Defaults suit a public deployment: any origin may
// call it (bundles are public data), the limit keys on Vercel's X-Real-Ip,
// which the platform overwrites on every request, and checkpoint files are
// cached under the only writable directory, the temp dir.
func fromEnv() (http.Handler, error) {
	n, err := network.Lookup(env("EXNODE_NETWORK", "public"))
	if err != nil {
		return nil, err
	}
	arch := archive.NewClient(env("EXNODE_ARCHIVE", n.Archive), env("EXNODE_CACHE", filepath.Join(os.TempDir(), "exnode")))
	s := &server.Server{
		Builder:        &bundle.Builder{Network: n.Passphrase, Archive: arch},
		Log:            slog.New(slog.NewJSONHandler(os.Stderr, nil)),
		ArchiveTip:     arch.Tip,
		RatePerSecond:  1,
		RateBurst:      10,
		ClientIPHeader: env("EXNODE_CLIENT_IP_HEADER", "X-Real-Ip"),
		// Stay inside the platform's 60-second function limit.
		BuildTimeout: 50 * time.Second,
	}
	for _, o := range strings.Split(env("EXNODE_CORS", "*"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			s.CORSOrigins = append(s.CORSOrigins, o)
		}
	}
	if u := os.Getenv("EXNODE_RPC"); u != "" {
		c := rpc.NewClient(u)
		s.Builder.RPC = c
		s.RPCLatest = c.LatestLedger
	}
	return s.Handler(), nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
