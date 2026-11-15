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
	gh     []string
	ghIn   string
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
		RunGh: func(stdin string, args ...string) error {
			run.ghIn, run.gh = stdin, args
			return nil
		},
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

func created() *mintStub { return &mintStub{status: 201, body: mintedBody} }

func TestTokensCreateSendsExactlyTheDocumentedBodyKeys(t *testing.T) {
	stub := created()
	runCreate(t, stub, false, createOptions{name: "ci", agent: true, org: "acme", repos: []string{"acme/web", "acme/api"}, scopes: []string{"read"}, expiresIn: "90d", toStdout: true})
	want := map[string]any{"name": "ci", "kind": "agent", "scopes": []any{"read"}, "organization": "acme", "repos": []any{"acme/web", "acme/api"}, "expires_in_s": float64(90 * 86400)}
	if len(stub.bodies) != 1 || !reflect.DeepEqual(stub.bodies[0], want) || stub.requests[0] != "POST /api/v1/tokens" {
		t.Errorf("requests %q bodies %v, want one POST with %v", stub.requests, stub.bodies, want)
	}
}

func TestTokensCreateOmitsEveryKeyTheUserDidNotSet(t *testing.T) {
	stub := created()
	runCreate(t, stub, false, createOptions{name: "ci", toStdout: true})
	if want := map[string]any{"name": "ci"}; !reflect.DeepEqual(stub.bodies[0], want) {
		t.Errorf("body = %v, want %v", stub.bodies[0], want)
	}
}

func TestTokensCreateExpiresInTakesDaysAndGoDurations(t *testing.T) {
	for in, want := range map[string]float64{"90d": 7776000, "12h": 43200, "1h30m": 5400, "45s": 45} {
		stub := created()
		runCreate(t, stub, false, createOptions{name: "ci", toStdout: true, expiresIn: in})
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
	cases := map[string]struct {
		tty bool
		o   createOptions
	}{
		"no sink":                   {o: createOptions{name: "ci"}},
		"two sinks":                 {o: createOptions{name: "ci", toStdout: true, output: "x"}},
		"three sinks":               {o: createOptions{name: "ci", toStdout: true, output: "x", githubSecret: "S", secretRepo: "a/b"}},
		"stdout on a terminal":      {tty: true, o: createOptions{name: "ci", toStdout: true}},
		"output already exists":     {o: createOptions{name: "ci", output: existing}},
		"github secret no repo":     {o: createOptions{name: "ci", githubSecret: "S", repos: []string{"a/b"}}},
		"stdout with json":          {o: createOptions{name: "ci", toStdout: true, json: true}},
		"secret repo with output":   {o: createOptions{name: "ci", output: filepath.Join(t.TempDir(), "o"), secretRepo: "a/b"}},
		"secret repo with stdout":   {o: createOptions{name: "ci", toStdout: true, secretRepo: "a/b"}},
		"secret repo on a dry run":  {o: createOptions{name: "ci", dryRun: true, secretRepo: "a/b"}},
		"two sinks on a dry run":    {o: createOptions{name: "ci", dryRun: true, toStdout: true, output: "x"}},
		"no name":                   {o: createOptions{toStdout: true}},
		"expires zero":              {o: createOptions{name: "ci", toStdout: true, expiresIn: "0d"}},
		"expires negative":          {o: createOptions{name: "ci", toStdout: true, expiresIn: "-5h"}},
		"expires fractional second": {o: createOptions{name: "ci", toStdout: true, expiresIn: "1500ms"}},
		"expires junk":              {o: createOptions{name: "ci", toStdout: true, expiresIn: "soon"}},
		"expires huge days":         {o: createOptions{name: "ci", toStdout: true, expiresIn: "99999999999d"}},
	}
	for name, c := range cases {
		stub := created()
		run := runCreate(t, stub, c.tty, c.o)
		if exitOf(run.err) != 5 || len(stub.requests) != 0 || run.stdout != "" {
			t.Errorf("%s: exit %d, %d requests, stdout %q; want exit 5 and nothing sent or printed (err %v)", name, exitOf(run.err), len(stub.requests), run.stdout, run.err)
		}
	}
}

func TestTokensCreateStdoutWritesTheSecretAndOneNewlineOnly(t *testing.T) {
	run := runCreate(t, created(), false, createOptions{name: "ci", toStdout: true})
	if run.err != nil || run.stdout != leakedSecret+"\n" {
		t.Errorf("err %v stdout %q, want exactly the secret and a newline", run.err, run.stdout)
	}
	if !strings.Contains(run.stderr, "sel_ab12") || !strings.Contains(run.stderr, "stdout") {
		t.Errorf("stderr %q, want a line naming the prefix and the sink", run.stderr)
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

func TestTokensCreateGithubSecretSendsTheSecretOnStdinNotArgv(t *testing.T) {
	run := runCreate(t, created(), false, createOptions{name: "ci", githubSecret: "SEL_TOKEN", secretRepo: "acme/web"})
	if run.err != nil || run.ghIn != leakedSecret || strings.Join(run.gh, " ") != "secret set SEL_TOKEN --repo acme/web" {
		t.Errorf("err %v gh stdin %q args %q, want the secret on stdin and a secret-free argv", run.err, run.ghIn, run.gh)
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
		"stdout":      {o: createOptions{name: "ci", toStdout: true}},
		"output json": {o: createOptions{name: "ci", output: filepath.Join(dir, "a"), json: true}},
		"output text": {tty: true, o: createOptions{name: "ci", output: filepath.Join(dir, "b")}},
		"github":      {o: createOptions{name: "ci", githubSecret: "S", secretRepo: "a/b"}},
		"sink fails":  {o: createOptions{name: "ci", output: filepath.Join(dir, "missing", "c")}},
	}
	for name, m := range modes {
		run := runCreate(t, created(), m.tty, m.o)
		leaked := run.stdout + run.stderr
		if run.err != nil {
			leaked += run.err.Error()
		}
		if name == "stdout" {
			leaked = strings.Replace(leaked, leakedSecret+"\n", "", 1)
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
		run := runCreate(t, &mintStub{status: c.status, body: c.body}, false, createOptions{name: "ci", toStdout: true})
		if exitOf(run.err) != c.exit || run.stdout != "" {
			t.Errorf("%d %s: exit %d stdout %q, want %d and empty stdout", c.status, c.body, exitOf(run.err), run.stdout, c.exit)
		}
	}
}

func TestTokensCreateStdoutKeepsErrorDocumentsOffStdoutAndExitsByCode(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		exit   int
		code   string
	}{
		{403, forbiddenBody, 6, "forbidden"},
		{409, `{"error":"name_taken","message":"in use"}`, 5, "name_taken"},
	} {
		stub := &mintStub{status: c.status, body: c.body}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "tokens", "create", "--name", "ci", "--stdout")

		if res.exit != c.exit || res.stdout != "" || !strings.Contains(res.stderr, `"error":"`+c.code+`"`) {
			t.Errorf("%d: exit %d stdout %q stderr %q, want exit %d, empty stdout and the code on stderr", c.status, res.exit, res.stdout, res.stderr, c.exit)
		}
	}
}

func TestTokensCreateStdoutLeavesStdoutEmptyWhenAnythingFailsBeforeTheRequest(t *testing.T) {
	cases := map[string][]string{
		"no name":         {"--stdout"},
		"bad expires":     {"--name", "ci", "--stdout", "--expires-in", "soon"},
		"two sinks":       {"--name", "ci", "--stdout", "--output", "x"},
		"json":            {"--name", "ci", "--stdout", "--json"},
		"secret repo":     {"--name", "ci", "--stdout", "--secret-repo", "a/b"},
		"unknown flag":    {"--bogus", "--name", "ci", "--stdout"},
		"stdout equals":   {"--name", "ci", "--stdout=true", "--expires-in", "0d"},
		"positional args": {"--name", "ci", "--stdout", "extra"},
	}
	for name, args := range cases {
		stub := created()
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, append([]string{"tokens", "create"}, args...)...)

		if res.exit != 5 || res.stdout != "" || !strings.Contains(res.stderr, `"error":"usage_error"`) || len(stub.requests) != 0 {
			t.Errorf("%s: exit %d stdout %q stderr %q requests %v, want exit 5, empty stdout, the code on stderr, no request", name, res.exit, res.stdout, res.stderr, stub.requests)
		}
	}
}

func TestTokensCreateStdoutPropertyHoldsWhenTheCommandIsNotResolved(t *testing.T) {
	cases := map[string]struct {
		args []string
		exit int
	}{
		"subcommand typo":          {[]string{"tokens", "creat", "--name", "x", "--stdout"}, 5},
		"flag before the command":  {[]string{"--stdout", "tokens", "create", "--name", "x"}, 5},
		"help flag":                {[]string{"tokens", "create", "--name", "x", "--stdout", "--help"}, 0},
		"short help flag":          {[]string{"tokens", "create", "--name", "x", "--stdout", "-h"}, 0},
		"help flag before stdout":  {[]string{"tokens", "create", "--help", "--stdout"}, 0},
		"typo in an unrelated cmd": {[]string{"nosuchcommand", "--stdout"}, 5},
	}
	for name, c := range cases {
		stub := created()
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, c.args...)

		if res.exit != c.exit || res.stdout != "" || res.stderr == "" || len(stub.requests) != 0 {
			t.Errorf("%s: exit %d stdout %q stderr %q requests %v, want exit %d with empty stdout and the text on stderr", name, res.exit, res.stdout, res.stderr, stub.requests, c.exit)
		}
	}
	plain := runBinary(t, t.TempDir(), "http://unused", "tokens", "create", "--help")
	if plain.exit != 0 || !strings.Contains(plain.stdout, "Create an API token") {
		t.Errorf("help without a secret flag: exit %d stdout %q, want it on stdout as before", plain.exit, plain.stdout)
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

func TestHelpListsTokensCreateAsAWriteWithASecretFlag(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path        string   `json:"path"`
				ExitCodes   []int    `json:"exit_codes"`
				Mutates     bool     `json:"mutates"`
				SecretFlags []string `json:"secret_flags"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, c := range doc.Help.Commands {
		if c.Path == "tokens create" {
			listed = len(c.ExitCodes) > 0 && c.Mutates && reflect.DeepEqual(c.SecretFlags, []string{"stdout"})
		}
	}
	if !listed {
		t.Error("help --json lacks tokens create with exit_codes, mutates and secret_flags")
	}
}
