package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/zalando/go-keyring"
)

const (
	storedToken = "sk_live_abcdef0123456789"
	meBody      = `{"user_id":"u1","email":"ben@example.com","org_slugs":["acme","beta"],"scopes":["read","setup"]}`
)

type selRun struct {
	host      string
	token     string
	stdin     string
	configDir string
}

func (r selRun) command(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	if r.configDir == "" {
		r.configDir = t.TempDir()
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"SELECTIVE_TEST_RUN_MAIN=1",
		"SELECTIVE_HOST="+r.host,
		"SELECTIVE_TOKEN="+r.token,
		"SELECTIVE_CONFIG_DIR="+r.configDir,
	)
	cmd.Stdin = strings.NewReader(r.stdin)
	return cmd
}

func (r selRun) run(t *testing.T, args ...string) cliResult {
	t.Helper()
	cmd := r.command(t, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	res := cliResult{}
	var exitErr *exec.ExitError
	if err := cmd.Run(); errors.As(err, &exitErr) {
		res.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	res.stdout, res.stderr = stdout.String(), stderr.String()
	return res
}

func meServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/me" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func storeInConfigFile(t *testing.T, host string) string {
	t.Helper()
	dir := t.TempDir()
	data, _ := json.Marshal(config.Config{CurrentHost: host, Hosts: map[string]*config.Host{host: {Token: storedToken}}})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

type statusDocument struct {
	V    int `json:"v"`
	Auth struct {
		Host        string   `json:"host"`
		Email       string   `json:"email"`
		Orgs        []string `json:"orgs"`
		TokenSource string   `json:"token_source"`
		Token       struct {
			Prefix string   `json:"prefix"`
			Scopes []string `json:"scopes"`
		} `json:"token"`
	} `json:"auth"`
	Scopes any `json:"scopes"`
}

func decodeStatus(t *testing.T, stdout string) statusDocument {
	t.Helper()
	var doc statusDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("stdout is not one JSON object: %v\n%q", err, stdout)
	}
	return doc
}

func TestAuthStatusPipedIsAJSONAuthObjectWithATokenObject(t *testing.T) {
	srv := meServer(t, 200, meBody)

	res := selRun{host: srv.URL, token: storedToken}.run(t, "auth", "status")

	doc := decodeStatus(t, res.stdout)
	if res.exit != 0 || doc.V != 1 || doc.Auth.Host != srv.URL || doc.Auth.Email != "ben@example.com" || strings.Join(doc.Auth.Orgs, ",") != "acme,beta" {
		t.Errorf("exit %d, doc %+v", res.exit, doc)
	}
	if doc.Auth.Token.Prefix != "sk_live_abcdef" || strings.Join(doc.Auth.Token.Scopes, ",") != "read,setup" || doc.Scopes != nil {
		t.Errorf("token = %+v, top-level scopes = %v", doc.Auth.Token, doc.Scopes)
	}
	if strings.Contains(res.stdout, storedToken) || res.stderr != "" {
		t.Errorf("stdout %q or stderr %q carries the token or noise", res.stdout, res.stderr)
	}
}

func TestAuthStatusOmitsPrefixAndScopesItDoesNotKnow(t *testing.T) {
	srv := meServer(t, 200, `{"email":"ben@example.com","org_slugs":null}`)

	res := selRun{host: srv.URL, token: "sk_live_short"}.run(t, "auth", "status")

	want := `{"v":1,"auth":{"host":"` + srv.URL + `","email":"ben@example.com","orgs":[],"token_source":"env","token":{}}}` + "\n"
	if res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q, want %q", res.exit, res.stdout, want)
	}
}

func TestAuthStatusTokenSource(t *testing.T) {
	srv := meServer(t, 200, meBody)

	t.Run("env", func(t *testing.T) {
		res := selRun{host: srv.URL, token: storedToken}.run(t, "auth", "status")
		if got := decodeStatus(t, res.stdout).Auth.TokenSource; got != "env" {
			t.Errorf("token_source = %q", got)
		}
	})
	t.Run("config", func(t *testing.T) {
		res := selRun{host: srv.URL, configDir: storeInConfigFile(t, srv.URL)}.run(t, "auth", "status")
		if got := decodeStatus(t, res.stdout).Auth.TokenSource; got != "config" {
			t.Errorf("token_source = %q (stdout %q)", got, res.stdout)
		}
	})
	t.Run("keychain", func(t *testing.T) {
		keyring.MockInit()
		if err := keyring.Set("selective-cli", srv.URL, storedToken); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SELECTIVE_HOST", srv.URL)
		t.Setenv("SELECTIVE_TOKEN", "")
		t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())

		code, stdout, _ := execute("auth", "status")

		if got := decodeStatus(t, stdout).Auth.TokenSource; got != "keychain" || code != 0 {
			t.Errorf("exit %d, token_source = %q", code, got)
		}
	})
}

func TestAuthStatusFailuresAreErrorObjectsOnStdout(t *testing.T) {
	unauthorized := meServer(t, 401, `{"error":"unauthorized","message":"Missing, invalid or revoked API token."}`)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cases := []struct {
		name string
		run  selRun
		exit int
		code string
	}{
		{"not logged in", selRun{host: unauthorized.URL}, 4, "unauthorized"},
		{"stored token rejected", selRun{host: unauthorized.URL, configDir: storeInConfigFile(t, unauthorized.URL)}, 4, "unauthorized"},
		{"env token rejected", selRun{host: unauthorized.URL, token: storedToken}, 4, "unauthorized"},
		{"server unreachable", selRun{host: closed.URL, token: storedToken}, 4, "network_error"},
		{"server forbids", selRun{host: meServer(t, 403, `{"error":"forbidden","message":"no"}`).URL, token: storedToken}, 6, "forbidden"},
		{"server answers 404", selRun{host: meServer(t, 404, `{}`).URL, token: storedToken}, 3, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.run.run(t, "auth", "status")

			lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
			if res.exit != tc.exit || len(lines) != 1 || !strings.HasPrefix(lines[0], `{"v":1,"error":"`+tc.code+`","message":"`) {
				t.Errorf("exit %d stdout %q, want exit %d with one %s error object", res.exit, res.stdout, tc.exit, tc.code)
			}
			if !strings.HasPrefix(res.stderr, "Error: ") {
				t.Errorf("stderr = %q, want Error: …", res.stderr)
			}
		})
	}
}

func TestLoginWithARejectedTokenIsUnauthorized(t *testing.T) {
	srv := meServer(t, 401, `{"error":"unauthorized","message":"Missing, invalid or revoked API token."}`)

	res := selRun{host: srv.URL, stdin: "sk_live_rejected00000\n"}.run(t, "auth", "login", "--with-token")

	if res.exit != 4 || !strings.HasPrefix(res.stdout, `{"v":1,"error":"unauthorized"`) {
		t.Errorf("exit %d stdout %q, want exit 4 with an unauthorized object", res.exit, res.stdout)
	}
}

func TestHelpDeclaresExitCodesAndEventsForTheAuthOrgsAndReposCommands(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path      string   `json:"path"`
				ExitCodes []int    `json:"exit_codes"`
				Events    []string `json:"events"`
				MCP       string   `json:"mcp"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	declared := map[string]int{}
	for _, c := range doc.Help.Commands {
		declared[c.Path] = len(c.ExitCodes)
		interactive := c.Path == "auth login" || c.Path == "auth signup"
		if interactive && (strings.Join(c.Events, ",") != "authorize,logged_in" || c.MCP != "skip") {
			t.Errorf("%q: events %v, mcp %q", c.Path, c.Events, c.MCP)
		}
	}
	for _, path := range []string{"auth login", "auth signup", "auth status", "auth logout", "orgs", "repos"} {
		if declared[path] == 0 {
			t.Errorf("%q declares no exit_codes in help --json", path)
		}
	}
}
