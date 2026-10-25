// Package api runs exnode as a Vercel serverless function: the same HTTP
// server as `exnode serve`, configured from the same EXNODE_* environment
// (see package hosted).
//
// vercel.json routes every path here, so a deployment answers the routes in
// openapi.yaml. Being untrusted by design, a hosted node needs no secrets:
// whatever it serves is checked against validator signatures by the client.
package api

import (
	"net/http"
	"sync"

	"github.com/Externalize-Labs/externalize-node/hosted"
)

var (
	once    sync.Once
	handler http.Handler
	initErr error
)

// Handler serves every exnode route. The server is built on the first
// request and reused while the function instance stays warm.
func Handler(w http.ResponseWriter, r *http.Request) {
	once.Do(func() { handler, initErr = hosted.FromEnv() })
	if initErr != nil {
		http.Error(w, "exnode is misconfigured: "+initErr.Error(), http.StatusInternalServerError)
		return
	}
	handler.ServeHTTP(w, r)
}
