// Package hosted builds exnode's HTTP server from the EXNODE_* environment,
// for serverless platforms that run a handler rather than `exnode serve`.
//
// It lives outside internal/ on purpose: platforms such as Vercel compile the
// function file as a package of their own, which Go does not allow to import
// this module's internal packages directly.
package hosted

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Externalize-Labs/externalize-node/internal/archive"
	"github.com/Externalize-Labs/externalize-node/internal/bundle"
	"github.com/Externalize-Labs/externalize-node/internal/network"
	"github.com/Externalize-Labs/externalize-node/internal/rpc"
	"github.com/Externalize-Labs/externalize-node/internal/server"
)

// FromEnv builds the server. Defaults suit a public deployment: any origin may
// call it (bundles are public data), the limit keys on Vercel's X-Real-Ip,
// which the platform overwrites on every request, and checkpoint files are
// cached under the only writable directory, the temp dir.
func FromEnv() (http.Handler, error) {
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
