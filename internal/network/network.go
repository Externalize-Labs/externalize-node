// Package network names the Stellar networks exnode knows and where their
// history is archived.
package network

import (
	"fmt"
	"strings"
)

// Network is one Stellar network.
type Network struct {
	Name       string
	Passphrase string
	// Archive lists SDF's three history archive mirrors, comma-separated.
	Archive string
}

var known = map[string]Network{
	"public":  {"public", "Public Global Stellar Network ; September 2015", sdfMirrors("core-live/core_live")},
	"testnet": {"testnet", "Test SDF Network ; September 2015", sdfMirrors("core-testnet/core_testnet")},
}

// Lookup returns the network called name ("public" or "testnet").
func Lookup(name string) (Network, error) {
	n, ok := known[name]
	if !ok {
		return Network{}, fmt.Errorf("unknown network %q (want public or testnet)", name)
	}
	return n, nil
}

// sdfMirrors lists SDF's three history archives for a network.
func sdfMirrors(prefix string) string {
	var urls []string
	for i := 1; i <= 3; i++ {
		urls = append(urls, fmt.Sprintf("https://history.stellar.org/prd/%s_%03d", prefix, i))
	}
	return strings.Join(urls, ",")
}
