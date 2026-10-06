package main

import (
	"strings"
	"testing"
)

func TestSDFMirrors(t *testing.T) {
	got := strings.Split(sdfMirrors("core-live/core_live"), ",")
	want := []string{
		"https://history.stellar.org/prd/core-live/core_live_001",
		"https://history.stellar.org/prd/core-live/core_live_002",
		"https://history.stellar.org/prd/core-live/core_live_003",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", got)
	}
}

func TestEveryNetworkHasMirrors(t *testing.T) {
	for name, n := range networks {
		if n.passphrase == "" || len(strings.Split(n.archive, ",")) != 3 {
			t.Errorf("%s: incomplete network definition", name)
		}
	}
}

func TestRepeatableFlags(t *testing.T) {
	var l list
	for _, v := range []string{"a", "b"} {
		if err := l.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if l.String() != "a,b" {
		t.Fatalf("got %q", l.String())
	}
}

func TestEnvFallback(t *testing.T) {
	t.Setenv("EXNODE_TEST_VALUE", "set")
	if env("EXNODE_TEST_VALUE", "default") != "set" || env("EXNODE_TEST_MISSING", "default") != "default" {
		t.Fatal("env fallback broken")
	}
	if defaultTo("", "0") != "0" || defaultTo("3", "0") != "3" {
		t.Fatal("defaultTo broken")
	}
}

func TestUnknownNetworkIsRejected(t *testing.T) {
	c := common{network: "moonnet"}
	if _, err := c.builder(); err == nil {
		t.Fatal("unknown network accepted")
	}
}
