package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyIntoGithubSecretWhenGhFailsIsAnInternalErrorWithoutASuccessDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api_key") {
			_, _ = w.Write([]byte(`{"api_key":"suite-key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"suites":[{"id":"s-1","name":"RSpec","type":"rspec","repo":"acme/web","last_run_at":null}]}`))
	}))
	t.Cleanup(srv.Close)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "key", "s-1", "--github-secret", "K", "--org", "acme", "--json")

	assertRefusal(t, res, 9, "internal_error")
	if strings.Contains(res.stdout, `"result"`) {
		t.Errorf("stdout carries a success document: %q", res.stdout)
	}
}
