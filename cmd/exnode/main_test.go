package main

import "testing"

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

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1500": 1500, "500MB": 500 << 20, "2gb": 2 << 30, " 3 KB ": 3 << 10, "7B": 7} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%q: got %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "MB", "-1GB", "1.5GB", "lots"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestUnknownNetworkIsRejected(t *testing.T) {
	c := common{network: "moonnet"}
	if _, err := c.builder(); err == nil {
		t.Fatal("unknown network accepted")
	}
}
