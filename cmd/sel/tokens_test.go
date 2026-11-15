package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	agentToken     = `{"id":"2","name":"ci","prefix":"sel_ab12","kind":"agent","scopes":["read","write"],"organization":"acme","repos":["acme/web","acme/api"],"expires_at":"2026-12-01T00:00:00Z","source":"api","inserted_at":"2026-11-01T09:30:00Z"}`
	userToken      = `{"id":"1","name":"laptop","prefix":"sel_cd34","kind":"user","scopes":["read"],"organization":null,"repos":null,"expires_at":null,"source":"cli","inserted_at":"2026-10-01T09:30:00Z"}`
	leakedSecret   = "sel_LEAKED_plaintext_secret"
	forbiddenBody  = `{"error":"forbidden","message":"Agent tokens can't manage other tokens."}`
	forbiddenStdio = `{"v":1,"error":"forbidden","message":"Agent tokens can't manage other tokens."}` + "\n"
)

type tokensStub struct {
	status int
	body   string
}

func (s *tokensStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/api/v1/tokens/current" {
			http.NotFound(w, r)
			return
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		fmt.Fprint(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTokensShowPrintsTheCurrentToken(t *testing.T) {
	srv := (&tokensStub{body: `{"token":` + agentToken + `}`}).start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "show", "--json")

	if want := `{"v":1,"token":` + agentToken + `}` + "\n"; res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q stderr %q", res.exit, res.stdout, want, res.stderr)
	}
}

func TestTokensShowTextShowsTheTokensScope(t *testing.T) {
	srv := (&tokensStub{body: `{"token":` + agentToken + `}`}).start(t)
	unrestricted := (&tokensStub{body: `{"token":` + userToken + `}`}).start(t)
	var out, errOut bytes.Buffer

	if err := tokensShow(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatal(err)
	}
	scoped := out.String()
	out.Reset()
	if err := tokensShow(&setup.Env{Client: api.New(unrestricted.URL, "tok"), Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"ci\n", "Prefix:    sel_ab12", "Kind:      agent", "Scopes:    read,write", "Org:       acme", "Repos:     acme/web,acme/api", "Expires:   2026-12-01 00:00", "Created:   2026-11-01 09:30 UTC"} {
		if !strings.Contains(scoped, want) {
			t.Errorf("show text missing %q:\n%s", want, scoped)
		}
	}
	for _, want := range []string{"Org:       all", "Repos:     all", "Expires:   never"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("unrestricted show text missing %q:\n%s", want, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestTokensShowNeverEchoesAPlaintextSecret(t *testing.T) {
	leaky := strings.TrimSuffix(agentToken, "}") + `,"token":"` + leakedSecret + `"}`
	srv := (&tokensStub{body: `{"token":` + leaky + `}`}).start(t)
	var jsonOut, textOut, errOut bytes.Buffer
	client := api.New(srv.URL, "tok")

	for _, env := range []*setup.Env{
		{Client: client, Stdout: &jsonOut, Stderr: &errOut, JSON: true},
		{Client: client, Stdout: &textOut, Stderr: &errOut},
	} {
		if err := tokensShow(env); err != nil {
			t.Fatal(err)
		}
	}

	for name, got := range map[string]string{"json stdout": jsonOut.String(), "text stdout": textOut.String(), "stderr": errOut.String()} {
		if strings.Contains(got, leakedSecret) {
			t.Errorf("%s echoes the plaintext: %q", name, got)
		}
	}
	var doc struct {
		Token map[string]any `json:"token"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &doc); err != nil || len(doc.Token) != 10 {
		t.Errorf("show --json token has %d fields, want the 10 contract fields (%v)", len(doc.Token), err)
	}
}

func TestTokensShowMapsServerRefusalsToExitCodes(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		exit   int
		stdout string
	}{
		{"forbidden", 403, forbiddenBody, []string{"tokens", "show", "--json"}, 6, forbiddenStdio},
		{"unauthorized", 401, `{"error":"unauthorized","message":"Invalid token."}`, []string{"tokens", "show", "--json"}, 4, `{"v":1,"error":"unauthorized","message":"Invalid token."}` + "\n"},
		{"bad request", 422, `{"error":"invalid_request","message":"nope"}`, []string{"tokens", "show", "--json"}, 5, `{"v":1,"error":"invalid_request","message":"nope"}` + "\n"},
		{"takes no argument", 200, `{}`, []string{"tokens", "show", "some-id", "--json"}, 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&tokensStub{status: tc.status, body: tc.body}).start(t)

			res := runBinary(t, t.TempDir(), srv.URL, tc.args...)

			if res.exit != tc.exit {
				t.Errorf("exit %d, want %d (stdout %q)", res.exit, tc.exit, res.stdout)
			}
			if tc.stdout != "" && res.stdout != tc.stdout {
				t.Errorf("stdout %q, want %q", res.stdout, tc.stdout)
			}
			if tc.stdout == "" && !strings.Contains(res.stdout, `"error":"usage_error"`) {
				t.Errorf("stdout %q, want a usage_error object", res.stdout)
			}
		})
	}
	t.Run("server unreachable", func(t *testing.T) {
		res := runBinary(t, t.TempDir(), closed.URL, "tokens", "show", "--json")

		assertRefusal(t, res, 4, "network_error")
	})
}

func TestHelpListsTokensShowAsARead(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path      string `json:"path"`
				ExitCodes []int  `json:"exit_codes"`
				Mutates   bool   `json:"mutates"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, c := range doc.Help.Commands {
		if c.Path == "tokens show" {
			listed = len(c.ExitCodes) > 0 && !c.Mutates
		}
	}
	if !listed {
		t.Error("help --json lacks tokens show with exit_codes and no mutates")
	}
}
