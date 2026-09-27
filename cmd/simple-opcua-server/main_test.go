package main

import (
	"os"
	"testing"

	opcuaserver "github.com/moriax/simple-opcua-server"
)

func TestParseAuthModes(t *testing.T) {
	got, err := parseAuthModes("anonymous")
	if err != nil || len(got) != 1 || got[0] != opcuaserver.AuthAnonymous {
		t.Errorf("parseAuthModes(anonymous) = (%v, %v)", got, err)
	}

	got, err = parseAuthModes(" anonymous , username ")
	if err != nil || len(got) != 2 || got[1] != opcuaserver.AuthUserName {
		t.Errorf("parseAuthModes with both = (%v, %v)", got, err)
	}

	if _, err := parseAuthModes("kerberos"); err == nil {
		t.Error("an unsupported token type should be rejected")
	}
	if _, err := parseAuthModes(""); err == nil {
		t.Error("an empty -auth should be rejected")
	}
}

// A fresh checkout has no customer tag file, so the CLI has to fall back to the
// example that ships with the repository.
func TestResolveConfigFallsBackToTheExample(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })

	got, err := resolveConfig(defaultConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(got) {
		t.Fatalf("resolveConfig returned %q, which does not exist", got)
	}

	if _, err := resolveConfig("config/definitely-absent.csv"); err == nil {
		t.Error("a named file that is missing should be an error, not a fallback")
	}
}
