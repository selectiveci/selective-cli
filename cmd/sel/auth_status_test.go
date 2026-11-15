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

func (r selRun) run(t *testing.T, args ...string) cliResult {
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

func TestAuthStatusPipedIsAJSONAuthObjectWithATokenObject(t *testing.T) {
	full := meServer(t, 200, meBody)
	bare := meServer(t, 200, `{"email":"ben@example.com","org_slugs":null}`)
	cases := []struct {
		name string
		run  selRun
		want string
	}{
		{"env token", selRun{host: full.URL, token: storedToken},
			`{"v":1,"auth":{"host":"` + full.URL + `","email":"ben@example.com","orgs":["acme","beta"],"token_source":"env","token":{"prefix":"sk_live_abcdef","scopes":["read","setup"]}}}`},
		{"config file token", selRun{host: full.URL, configDir: storeInConfigFile(t, full.URL)},
			`{"v":1,"auth":{"host":"` + full.URL + `","email":"ben@example.com","orgs":["acme","beta"],"token_source":"config","token":{"prefix":"sk_live_abcdef","scopes":["read","setup"]}}}`},
		{"short token, no scopes, no orgs", selRun{host: bare.URL, token: "sk_live_short"},
			`{"v":1,"auth":{"host":"` + bare.URL + `","email":"ben@example.com","orgs":[],"token_source":"env","token":{}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.run.run(t, "auth", "status")

			if res.exit != 0 || res.stdout != tc.want+"\n" || res.stderr != "" {
				t.Errorf("exit %d stdout %q stderr %q, want exit 0 and %q", res.exit, res.stdout, res.stderr, tc.want)
			}
		})
	}
}

func TestAuthStatusReportsAKeychainTokenSource(t *testing.T) {
	srv := meServer(t, 200, meBody)
	keyring.MockInit()
	if err := keyring.Set("selective-cli", srv.URL, storedToken); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SELECTIVE_HOST", srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())

	code, stdout, _ := execute("auth", "status")

	if code != 0 || !strings.Contains(stdout, `"token_source":"keychain"`) {
		t.Errorf("exit %d stdout %q", code, stdout)
	}
}

func TestAuthStatusFailuresAreErrorObjectsOnStdout(t *testing.T) {
	unauthorized := meServer(t, 401, `{"error":"unauthorized","message":"Missing, invalid or revoked API token."}`)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cases := []struct {
		name    string
		run     selRun
		exit    int
		code    string
		message string
	}{
		{"not logged in", selRun{host: unauthorized.URL}, 4, "unauthorized", "run: sel auth login --hostname " + unauthorized.URL},
		{"stored token rejected", selRun{host: unauthorized.URL, configDir: storeInConfigFile(t, unauthorized.URL)}, 4, "unauthorized", "run: sel auth login --hostname " + unauthorized.URL},
		{"env token rejected", selRun{host: unauthorized.URL, token: storedToken}, 4, "unauthorized", "rejected"},
		{"server unreachable", selRun{host: closed.URL, token: storedToken}, 4, "network_error", "could not reach"},
		{"server forbids", selRun{host: meServer(t, 403, `{"error":"forbidden","message":"no"}`).URL, token: storedToken}, 6, "forbidden", "no"},
		{"server answers 404", selRun{host: meServer(t, 404, `{}`).URL, token: storedToken}, 3, "not_found", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.run.run(t, "auth", "status")

			var doc struct {
				V       int    `json:"v"`
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			err := json.Unmarshal([]byte(res.stdout), &doc)
			if err != nil || strings.Count(res.stdout, "\n") != 1 || res.exit != tc.exit || doc.V != 1 || doc.Error != tc.code || !strings.Contains(doc.Message, tc.message) {
				t.Errorf("exit %d stdout %q (%v), want exit %d, %s, message containing %q", res.exit, res.stdout, err, tc.exit, tc.code, tc.message)
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
