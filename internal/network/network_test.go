package network

import (
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	pub, err := Lookup("public")
	if err != nil {
		t.Fatal(err)
	}
	if pub.Passphrase != "Public Global Stellar Network ; September 2015" {
		t.Fatalf("passphrase %q", pub.Passphrase)
	}
	want := []string{
		"https://history.stellar.org/prd/core-live/core_live_001",
		"https://history.stellar.org/prd/core-live/core_live_002",
		"https://history.stellar.org/prd/core-live/core_live_003",
	}
	if got := strings.Split(pub.Archive, ","); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mirrors %v", got)
	}
	test, err := Lookup("testnet")
	if err != nil || !strings.Contains(test.Archive, "core-testnet/core_testnet_003") {
		t.Fatalf("testnet %+v, %v", test, err)
	}
	if _, err := Lookup("futurenet"); err == nil {
		t.Fatal("an unknown network must be an error")
	}
}
