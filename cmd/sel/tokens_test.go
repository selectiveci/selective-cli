package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	mu      sync.Mutex
	cursors []string
	status  int
	body    string
	pages   map[string]string
}

func (s *tokensStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasPrefix(r.URL.Path, "/api/v1/tokens") {
			http.NotFound(w, r)
			return
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		if r.URL.Path == "/api/v1/tokens" && s.pages != nil {
			cursor := r.URL.Query().Get("cursor")
			s.cursors = append(s.cursors, cursor)
			fmt.Fprint(w, s.pages[cursor])
			return
		}
		fmt.Fprint(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func twoPages() map[string]string {
	return map[string]string{
		"":       `{"tokens":[` + userToken + `],"next_cursor":"page-2"}`,
		"page-2": `{"tokens":[` + agentToken + `],"next_cursor":null}`,
	}
}

func TestTokensListWalksEveryPageAndPrintsOneDocument(t *testing.T) {
	stub := &tokensStub{pages: twoPages()}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "list", "--json")

	want := `{"v":1,"tokens":[` + userToken + `,` + agentToken + `],"next_cursor":null}` + "\n"
	if res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q stderr %q", res.exit, res.stdout, want, res.stderr)
	}
	if strings.Join(stub.cursors, ",") != ",page-2" {
		t.Errorf("cursors sent = %q, want first page then page-2", stub.cursors)
	}
}

func TestTokensListWithNoTokensPrintsAnEmptyList(t *testing.T) {
	srv := (&tokensStub{pages: map[string]string{"": `{"tokens":[],"next_cursor":null}`}}).start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "list", "--json")

	if want := `{"v":1,"tokens":[],"next_cursor":null}` + "\n"; res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q", res.exit, res.stdout, want)
	}
}

func TestTokensListTextShowsEachTokensScope(t *testing.T) {
	srv := (&tokensStub{pages: twoPages()}).start(t)
	var out, errOut bytes.Buffer

	if err := tokensList(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"NAME", "laptop", "sel_cd34", "user", "read", "all", "never", "ci", "agent", "read,write", "acme", "acme/web,acme/api", "2026-12-01 00:00"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list text missing %q:\n%s", want, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
}

func TestTokensListNeverEchoesAPlaintextSecret(t *testing.T) {
	leaky := strings.Replace(userToken, `"source"`, `"token":"`+leakedSecret+`","source"`, 1)
	srv := (&tokensStub{pages: map[string]string{"": `{"tokens":[` + leaky + `],"next_cursor":null,"token":"` + leakedSecret + `"}`}}).start(t)
	var jsonOut, textOut, errOut bytes.Buffer
	client := api.New(srv.URL, "tok")

	for _, env := range []*setup.Env{
		{Client: client, Stdout: &jsonOut, Stderr: &errOut, JSON: true},
		{Client: client, Stdout: &textOut, Stderr: &errOut},
	} {
		if err := tokensList(env); err != nil {
			t.Fatal(err)
		}
	}

	for name, got := range map[string]string{"json stdout": jsonOut.String(), "text stdout": textOut.String(), "stderr": errOut.String()} {
		if strings.Contains(got, leakedSecret) {
			t.Errorf("%s echoes the plaintext: %q", name, got)
		}
	}
}

func TestTokensListMapsServerRefusalsToExitCodes(t *testing.T) {
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
		{"forbidden to agent tokens", 403, forbiddenBody, []string{"tokens", "list", "--json"}, 6, forbiddenStdio},
		{"unauthorized", 401, `{"error":"unauthorized","message":"Invalid token."}`, []string{"tokens", "list", "--json"}, 4, `{"v":1,"error":"unauthorized","message":"Invalid token."}` + "\n"},
		{"bad request", 422, `{"error":"invalid_request","message":"limit must be an integer from 1 to 200"}`, []string{"tokens", "list", "--json"}, 5, `{"v":1,"error":"invalid_request","message":"limit must be an integer from 1 to 200"}` + "\n"},
		{"takes no argument", 200, `{}`, []string{"tokens", "list", "extra", "--json"}, 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&tokensStub{status: tc.status, body: tc.body, pages: map[string]string{"": tc.body}}).start(t)

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
		res := runBinary(t, t.TempDir(), closed.URL, "tokens", "list", "--json")

		assertRefusal(t, res, 4, "network_error")
	})
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

func TestHelpListsTokensListAndShowAsReads(t *testing.T) {
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
	listed := map[string]bool{}
	for _, c := range doc.Help.Commands {
		if c.Path == "tokens list" || c.Path == "tokens show" {
			listed[c.Path] = len(c.ExitCodes) > 0 && !c.Mutates
		}
	}
	if !listed["tokens list"] || !listed["tokens show"] {
		t.Errorf("help --json tokens commands = %v, want both with exit_codes and no mutates", listed)
	}
}

const (
	revokeID      = "7d0c6a0e-5b1f-4c5e-9d0f-2a1b3c4d5e6f"
	revokeOKBody  = `{"token":{"id":"` + revokeID + `","name":"ci","prefix":"sel_ab12"},"result":"deleted","dry_run":false}`
	notFoundBody  = `{"error":"not_found","message":"No such token."}`
	notFoundStdio = `{"v":1,"error":"not_found","message":"No such token."}` + "\n"
)

type revokeStub struct {
	mu       sync.Mutex
	requests []string
	status   int
	body     string
}

func (s *revokeStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		fmt.Fprint(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTokensRevokeDeletesTheTokenAndPrintsAWriteObject(t *testing.T) {
	stub := &revokeStub{status: 200, body: revokeOKBody}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--yes", "--json")

	want := `{"v":1,"token":{"id":"` + revokeID + `","name":"ci","prefix":"sel_ab12"},"result":"deleted","dry_run":false}` + "\n"
	if res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q stderr %q", res.exit, res.stdout, want, res.stderr)
	}
	if got := strings.Join(stub.requests, "|"); got != "DELETE /api/v1/tokens/"+revokeID {
		t.Errorf("requests = %q, want one plain DELETE", got)
	}
}

func TestTokensRevokeWithoutYesIsAUsageErrorAndSendsNothing(t *testing.T) {
	stub := &revokeStub{status: 200, body: revokeOKBody}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--json")

	assertRefusal(t, res, 5, "usage_error")
	if len(stub.requests) != 0 {
		t.Errorf("requests = %v, want none", stub.requests)
	}
}

func TestTokensRevokeDryRunNeedsNoYesAndSendsDryRunTrue(t *testing.T) {
	stub := &revokeStub{status: 200, body: `{"token":{"id":"` + revokeID + `","name":"ci","prefix":"sel_ab12"},"result":"deleted","dry_run":true}`}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--dry-run", "--json")

	if res.exit != 0 || !strings.HasSuffix(strings.TrimSpace(res.stdout), `"result":"deleted","dry_run":true}`) {
		t.Errorf("exit %d stdout %q", res.exit, res.stdout)
	}
	if got := strings.Join(stub.requests, "|"); got != "DELETE /api/v1/tokens/"+revokeID+"?dry_run=true" {
		t.Errorf("requests = %q, want one DELETE with dry_run=true", got)
	}
}

func TestTokensRevokeOfAMissingTokenExitsThreeUnlessIfExists(t *testing.T) {
	stub := &revokeStub{status: 404, body: notFoundBody}
	srv := stub.start(t)

	missing := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--yes", "--json")
	tolerated := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--yes", "--if-exists", "--json")

	if missing.exit != 3 || missing.stdout != notFoundStdio {
		t.Errorf("without --if-exists: exit %d stdout %q", missing.exit, missing.stdout)
	}
	want := `{"v":1,"token":{"id":"` + revokeID + `"},"result":"unchanged","dry_run":false}` + "\n"
	if tolerated.exit != 0 || tolerated.stdout != want {
		t.Errorf("with --if-exists: exit %d stdout %q want %q", tolerated.exit, tolerated.stdout, want)
	}
}

func TestTokensRevokeIfExistsStillFailsOnOtherRefusals(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		exit   int
		stdout string
	}{
		{"forbidden", 403, forbiddenBody, 6, forbiddenStdio},
		{"unauthorized", 401, `{"error":"unauthorized","message":"Invalid token."}`, 4, `{"v":1,"error":"unauthorized","message":"Invalid token."}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&revokeStub{status: tc.status, body: tc.body}).start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--yes", "--if-exists", "--json")

			if res.exit != tc.exit || res.stdout != tc.stdout {
				t.Errorf("exit %d stdout %q, want exit %d stdout %q", res.exit, res.stdout, tc.exit, tc.stdout)
			}
		})
	}
}

func TestTokensRevokeTakesExactlyOneID(t *testing.T) {
	stub := &revokeStub{status: 200, body: revokeOKBody}
	srv := stub.start(t)

	none := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", "--yes", "--json")
	two := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", "a", "b", "--yes", "--json")

	assertRefusal(t, none, 5, "usage_error")
	assertRefusal(t, two, 5, "usage_error")
	if len(stub.requests) != 0 {
		t.Errorf("requests = %v, want none", stub.requests)
	}
}

func TestTokensRevokeTextReportsOnStderrAndKeepsStdoutEmpty(t *testing.T) {
	srv := (&revokeStub{status: 200, body: revokeOKBody}).start(t)
	gone := (&revokeStub{status: 404, body: notFoundBody}).start(t)
	var out, errOut bytes.Buffer

	if err := tokensRevoke(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}, revokeID, revokeOptions{yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := tokensRevoke(&setup.Env{Client: api.New(gone.URL, "tok"), Stdout: &out, Stderr: &errOut}, revokeID, revokeOptions{yes: true, ifExists: true}); err != nil {
		t.Fatal(err)
	}

	want := "Revoked token ci (sel_ab12).\nToken " + revokeID + " is already gone.\n"
	if out.Len() != 0 || errOut.String() != want {
		t.Errorf("stdout %q stderr %q, want empty and %q", out.String(), errOut.String(), want)
	}
}

func TestHelpListsTokensRevokeAsADestructiveWrite(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path        string `json:"path"`
				ExitCodes   []int  `json:"exit_codes"`
				Mutates     bool   `json:"mutates"`
				Destructive bool   `json:"destructive"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, c := range doc.Help.Commands {
		if c.Path == "tokens revoke" {
			listed = len(c.ExitCodes) > 0 && c.Mutates && c.Destructive
		}
	}
	if !listed {
		t.Error("help --json lacks tokens revoke with exit_codes, mutates and destructive")
	}
}
