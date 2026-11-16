package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKeyIntoGithubSecretWhenGhFailsIsAnInternalErrorWithoutASuccessDocument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a POSIX shell script")
	}
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

type keyHelp struct {
	Command struct {
		ExitCodes   []int    `json:"exit_codes"`
		SecretFlags []string `json:"secret_flags"`
	} `json:"command"`
}

func suitesKeyHelp(t *testing.T) keyHelp {
	t.Helper()
	_, stdout, _ := execute("help", "suites", "key", "--json")
	var doc keyHelp
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("help suites key --json: %v\n%s", err, stdout)
	}
	return doc
}

func keyServer(t *testing.T, suitesStatus int, suites string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case suitesStatus != http.StatusOK:
			w.WriteHeader(suitesStatus)
			_, _ = w.Write([]byte(`{"message":"no"}`))
		case strings.HasSuffix(r.URL.Path, "/api_key"):
			_, _ = w.Write([]byte(`{"api_key":"suite-key"}`))
		default:
			_, _ = w.Write([]byte(suites))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const oneSuite = `{"suites":[{"id":"s-1","name":"RSpec","type":"rspec","repo":"acme/web","last_run_at":null}]}`

func failingGhOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSuitesKeyHelpNamesStdoutAsASecretFlag(t *testing.T) {
	if got := suitesKeyHelp(t).Command.SecretFlags; len(got) != 1 || got[0] != "stdout" {
		t.Errorf("secret_flags = %v, want [stdout]", got)
	}
}

func TestSuitesKeyHelpListsExactlyTheExitCodesItCanProduce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a POSIX shell script")
	}
	failingGhOnPath(t)
	cases := map[string]struct {
		status int
		suites string
		args   []string
		exit   int
	}{
		"key written":      {200, oneSuite, []string{"s-1", "--stdout"}, 0},
		"unknown suite":    {200, `{"suites":[]}`, []string{"s-1", "--stdout"}, 3},
		"bad token":        {401, "", []string{"s-1", "--stdout"}, 4},
		"no destination":   {200, oneSuite, []string{"s-1"}, 5},
		"forbidden":        {403, "", []string{"s-1", "--stdout"}, 6},
		"gh cannot set it": {200, oneSuite, []string{"s-1", "--github-secret", "K"}, 9},
	}
	reached := map[int]bool{}
	for name, c := range cases {
		srv := keyServer(t, c.status, c.suites)
		res := runBinary(t, t.TempDir(), srv.URL, append(append([]string{"suites", "key"}, c.args...), "--org", "acme", "--json")...)
		if res.exit != c.exit {
			t.Errorf("%s: exit %d, want %d (stdout %q stderr %q)", name, res.exit, c.exit, res.stdout, res.stderr)
		}
		reached[res.exit] = true
	}
	declared := suitesKeyHelp(t).Command.ExitCodes
	if len(declared) != len(reached) {
		t.Errorf("exit_codes = %v, tests reach %v", declared, reached)
	}
	for _, code := range declared {
		if !reached[code] {
			t.Errorf("exit_codes lists %d, which no case reaches", code)
		}
	}
}

func TestSuitesKeyStdoutErrorObjectGoesToStderrAndLeavesStdoutEmpty(t *testing.T) {
	srv := keyServer(t, http.StatusOK, `{"suites":[]}`)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "key", "nope", "--stdout", "--org", "acme", "--json")

	if res.exit != 3 || res.stdout != "" || !strings.Contains(res.stderr, `"error":"suite_not_found"`) {
		t.Errorf("exit %d stdout %q stderr %q, want 3, empty stdout and the error object on stderr", res.exit, res.stdout, res.stderr)
	}
}

func TestSuitesKeyGithubSecretErrorObjectStaysOnStdout(t *testing.T) {
	srv := keyServer(t, http.StatusOK, `{"suites":[]}`)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "key", "nope", "--github-secret", "K", "--org", "acme", "--json")

	assertRefusal(t, res, 3, "suite_not_found")
}
