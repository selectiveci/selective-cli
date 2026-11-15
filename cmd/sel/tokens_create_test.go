package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	mintedBody = `{"token":{"id":"9","name":"ci","prefix":"sel_ab12","kind":"agent","scopes":["read"],"organization":"acme","repos":["acme/web"],"expires_at":"2026-12-01T00:00:00Z","source":"api","inserted_at":"2026-11-01T09:30:00Z","token":"` + leakedSecret + `"},"result":"created","dry_run":false}`
	dryRunBody = `{"token":{"name":"ci","kind":"agent","scopes":["read"],"organization":"acme","repos":["acme/web"],"expires_at":"2026-12-01T00:00:00Z","source":"api"},"result":"created","dry_run":true}`
)

type mintStub struct {
	mu       sync.Mutex
	requests []string
	bodies   []map[string]any
	status   int
	body     string
}

func (s *mintStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		fmt.Fprint(w, s.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type mintRun struct {
	err    error
	stdout string
	stderr string
}

func runCreate(t *testing.T, stub *mintStub, tty bool, o createOptions) mintRun {
	t.Helper()
	srv := stub.start(t)
	var out, errOut bytes.Buffer
	run := mintRun{}
	env := &setup.Env{
		Client:           api.New(srv.URL, "tok"),
		Stdout:           &out,
		Stderr:           &errOut,
		JSON:             o.json || !tty,
		StdoutIsTerminal: tty,
	}
	run.err = tokensCreate(env, o)
	run.stdout, run.stderr = out.String(), errOut.String()
	return run
}

func exitOf(err error) int {
	var coded contract.Coded
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}

func sinkPath(t *testing.T) string { return filepath.Join(t.TempDir(), "sink.token") }

func created() *mintStub { return &mintStub{status: 201, body: mintedBody} }

func TestTokensCreateSendsExactlyTheDocumentedBodyKeys(t *testing.T) {
	stub := created()
	runCreate(t, stub, false, createOptions{name: "ci", agent: true, org: "acme", repos: []string{"acme/web", "acme/api"}, scopes: []string{"read"}, expiresIn: "90d", output: sinkPath(t)})
	want := map[string]any{"name": "ci", "kind": "agent", "scopes": []any{"read"}, "organization": "acme", "repos": []any{"acme/web", "acme/api"}, "expires_in_s": float64(90 * 86400)}
	if len(stub.bodies) != 1 || !reflect.DeepEqual(stub.bodies[0], want) || stub.requests[0] != "POST /api/v1/tokens" {
		t.Errorf("requests %q bodies %v, want one POST with %v", stub.requests, stub.bodies, want)
	}
}

func TestTokensCreateOmitsEveryKeyTheUserDidNotSet(t *testing.T) {
	stub := created()
	runCreate(t, stub, false, createOptions{name: "ci", output: sinkPath(t)})
	if want := map[string]any{"name": "ci"}; !reflect.DeepEqual(stub.bodies[0], want) {
		t.Errorf("body = %v, want %v", stub.bodies[0], want)
	}
}

func TestTokensCreateExpiresInTakesDaysAndGoDurations(t *testing.T) {
	for in, want := range map[string]float64{"90d": 7776000, "12h": 43200, "1h30m": 5400, "45s": 45} {
		stub := created()
		runCreate(t, stub, false, createOptions{name: "ci", output: sinkPath(t), expiresIn: in})
		if got := stub.bodies[0]["expires_in_s"]; got != want {
			t.Errorf("--expires-in %s sent %v, want %v", in, got, want)
		}
	}
}

func TestTokensCreateRefusesBadInputBeforeAnyRequest(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "taken")
	if err := os.WriteFile(existing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]createOptions{
		"no sink":                   {name: "ci"},
		"output already exists":     {name: "ci", output: existing},
		"no name":                   {output: sinkPath(t)},
		"expires zero":              {name: "ci", output: sinkPath(t), expiresIn: "0d"},
		"expires negative":          {name: "ci", output: sinkPath(t), expiresIn: "-5h"},
		"expires fractional second": {name: "ci", output: sinkPath(t), expiresIn: "1500ms"},
		"expires junk":              {name: "ci", output: sinkPath(t), expiresIn: "soon"},
		"expires huge days":         {name: "ci", output: sinkPath(t), expiresIn: "99999999999d"},
	}
	for name, o := range cases {
		stub := created()
		run := runCreate(t, stub, false, o)
		if exitOf(run.err) != 5 || len(stub.requests) != 0 || run.stdout != "" {
			t.Errorf("%s: exit %d, %d requests, stdout %q; want exit 5 and nothing sent or printed (err %v)", name, exitOf(run.err), len(stub.requests), run.stdout, run.err)
		}
	}
}

func TestTokensCreateOutputWritesAPrivateFileAndAMetadataDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci.token")
	run := runCreate(t, created(), false, createOptions{name: "ci", output: path})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if info.Mode().Perm() != 0o600 || string(data) != leakedSecret+"\n" {
		t.Errorf("file mode %v content %q, want 0600 and the secret", info.Mode().Perm(), data)
	}
	var doc struct {
		V      int            `json:"v"`
		Token  map[string]any `json:"token"`
		Result string         `json:"result"`
		DryRun bool           `json:"dry_run"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &doc); err != nil || doc.V != 1 || doc.Result != "created" || doc.DryRun || doc.Token["id"] != "9" || doc.Token["prefix"] != "sel_ab12" {
		t.Errorf("stdout %q (%v), want a v1 created document with id and prefix", run.stdout, err)
	}
}

func TestTokensCreateDryRunSendsDryRunWritesNothingAndNeedsNoSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never")
	for _, o := range []createOptions{{name: "ci", dryRun: true}, {name: "ci", dryRun: true, output: path}} {
		stub := &mintStub{status: 200, body: dryRunBody}
		run := runCreate(t, stub, false, o)
		if run.err != nil || stub.requests[0] != "POST /api/v1/tokens?dry_run=true" || !strings.Contains(run.stdout, `"dry_run":true`) {
			t.Errorf("err %v requests %q stdout %q, want one dry-run POST and a dry_run document", run.err, stub.requests, run.stdout)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a dry run created the --output file")
	}
}

func TestTokensCreateNeverPrintsThePlaintextOutsideTheSink(t *testing.T) {
	dir := t.TempDir()
	modes := map[string]struct {
		tty bool
		o   createOptions
	}{
		"output json": {o: createOptions{name: "ci", output: filepath.Join(dir, "a"), json: true}},
		"output text": {tty: true, o: createOptions{name: "ci", output: filepath.Join(dir, "b")}},
		"sink fails":  {o: createOptions{name: "ci", output: filepath.Join(dir, "missing", "c")}},
	}
	for name, m := range modes {
		run := runCreate(t, created(), m.tty, m.o)
		leaked := run.stdout + run.stderr
		if run.err != nil {
			leaked += run.err.Error()
		}
		if strings.Contains(leaked, leakedSecret) {
			t.Errorf("%s: the plaintext appears outside the sink: %q", name, leaked)
		}
	}
}

func TestTokensCreateSinkFailureAfterTheMintExitsNineNamingTheToken(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "missing", "ci.token")
	run := runCreate(t, created(), false, createOptions{name: "ci", output: bad})
	if exitOf(run.err) != 9 || !strings.Contains(run.err.Error(), "id 9") || !strings.Contains(run.err.Error(), "sel_ab12") || run.stdout != "" {
		t.Errorf("exit %d err %v stdout %q, want exit 9 naming id and prefix with empty stdout", exitOf(run.err), run.err, run.stdout)
	}
}

func TestTokensCreateMapsServerRefusalsToExitCodes(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		exit   int
	}{
		{409, `{"error":"name_taken","message":"in use"}`, 5},
		{403, `{"error":"token_scope_exceeded","message":"too wide"}`, 6},
		{403, forbiddenBody, 6},
		{422, `{"error":"invalid_scope","message":"bad scope"}`, 5},
		{401, `{"error":"unauthorized","message":"no"}`, 4},
	} {
		run := runCreate(t, &mintStub{status: c.status, body: c.body}, false, createOptions{name: "ci", output: sinkPath(t)})
		if exitOf(run.err) != c.exit || run.stdout != "" {
			t.Errorf("%d %s: exit %d stdout %q, want %d and empty stdout", c.status, c.body, exitOf(run.err), run.stdout, c.exit)
		}
	}
}

func TestTokensCreateRunsEndToEndThroughTheBinary(t *testing.T) {
	srv := created().start(t)
	path := filepath.Join(t.TempDir(), "ci.token")

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "create", "--name", "ci", "--output", path)
	refused := runBinary(t, t.TempDir(), srv.URL, "tokens", "create", "--name", "ci")

	if res.exit != 0 || !strings.HasPrefix(res.stdout, `{"v":1,"token":{`) || strings.Contains(res.stdout+res.stderr, leakedSecret) {
		t.Errorf("exit %d stdout %q stderr %q, want a document without the plaintext", res.exit, res.stdout, res.stderr)
	}
	assertRefusal(t, refused, 5, "usage_error")
}

func TestHelpListsTokensCreateAsAWrite(t *testing.T) {
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
		if c.Path == "tokens create" {
			listed = len(c.ExitCodes) > 0 && c.Mutates
		}
	}
	if !listed {
		t.Error("help --json lacks tokens create with exit_codes and mutates")
	}
}
