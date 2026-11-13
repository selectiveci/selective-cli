package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/zalando/go-keyring"
)

const secretKey = "suite-key-should-not-show"

func fakeServer(t *testing.T, orgs string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/orgs":
			_, _ = w.Write([]byte(orgs))
		case r.URL.Path == "/api/v1/orgs/acme/suites" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"suites":[{"id":"s-1","name":"RSpec","type":"rspec","repo":"acme/web","last_run_at":null},{"id":"s-2","name":"Bare","type":"rspec","repo":null,"last_run_at":null}]}`))
		case r.URL.Path == "/api/v1/orgs/acme/repos":
			_, _ = w.Write([]byte(`{"github_account":"acme","install_url":"http://x/acme/github/install","repos":[{"full_name":"acme/web","private":true}]}`))
		case r.URL.Path == "/api/v1/orgs/acme/suites" && r.Method == "POST":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["name"] == "New" {
				_, _ = w.Write([]byte(`{"suite":{"id":"s-3","name":"New","type":"rspec","repo":"acme/web","last_run_at":null},"result":"created"}`))
				return
			}
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":"repo_not_installed","message":"The Selective GitHub App can't see this repository.","install_url":"http://x/acme/github/install"}`))
		case r.URL.Path == "/api/v1/orgs/acme/suites/s-1" && r.Method == "PATCH":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mode, _ := body["selection_mode"].(string)
			selects, _ := body["selection_on_default_branch"].(bool)
			_, _ = fmt.Fprintf(w, `{"suite":{"id":"s-1","name":"RSpec","selection_mode":%q,"selection_on_default_branch":%t},"previous":{"selection_mode":"off","selection_on_default_branch":false}}`, mode, selects)
		case strings.HasPrefix(r.URL.Path, "/api/v1/orgs/acme/suites/s-") && strings.HasSuffix(r.URL.Path, "/api_key"):
			_, _ = w.Write([]byte(`{"api_key":"` + secretKey + `"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(t *testing.T, orgs string) (*Env, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &Env{
		Client: api.New(fakeServer(t, orgs).URL, "sk_live_x"),
		Stdout: out,
		Stderr: &bytes.Buffer{},
	}, out
}

const oneOrg = `{"organizations":[{"slug":"acme","name":"Acme","role":"owner"}]}`

func TestKeyNeedsADestination(t *testing.T) {
	e, out := env(t, oneOrg)
	err := e.Key("acme", "rspec", KeyOptions{})
	if err == nil || !strings.Contains(err.Error(), "--stdout") {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
	if err := e.Key("acme", "rspec", KeyOptions{Stdout: true, GithubSecret: "X"}); err == nil {
		t.Fatal("accepted both destinations")
	}
}

func TestKeyNeverReachesATerminal(t *testing.T) {
	e, out := env(t, oneOrg)
	e.StdoutIsTerminal = true
	err := e.Key("acme", "rspec", KeyOptions{Stdout: true})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out.String(), secretKey) {
		t.Fatal("key printed to the terminal")
	}
}

func TestKeyGoesDownAPipe(t *testing.T) {
	e, out := env(t, oneOrg)
	if err := e.Key("acme", "rspec", KeyOptions{Stdout: true}); err != nil {
		t.Fatal(err)
	}
	if out.String() != secretKey {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestKeyIntoGithubSecretOnlyOnStdin(t *testing.T) {
	e, out := env(t, oneOrg)
	e.StdoutIsTerminal = true
	var gotStdin string
	var gotArgs []string
	e.RunGh = func(stdin string, args ...string) error {
		gotStdin, gotArgs = stdin, args
		return nil
	}
	if err := e.Key("acme", "s-1", KeyOptions{GithubSecret: "SELECTIVE_API_KEY"}); err != nil {
		t.Fatal(err)
	}
	if gotStdin != secretKey {
		t.Fatalf("gh stdin = %q", gotStdin)
	}
	if strings.Join(gotArgs, " ") != "secret set SELECTIVE_API_KEY --repo acme/web" {
		t.Fatalf("gh args = %v", gotArgs)
	}
	if strings.Contains(out.String(), secretKey) {
		t.Fatal("key printed")
	}
}

func TestResolveOrg(t *testing.T) {
	t.Setenv("SELECTIVE_ORG", "")
	e, _ := env(t, oneOrg)
	if org, err := e.ResolveOrg(""); err != nil || org != "acme" {
		t.Fatalf("one org: %q, %v", org, err)
	}

	e, _ = env(t, `{"organizations":[{"slug":"acme"},{"slug":"globex"}]}`)
	if _, err := e.ResolveOrg(""); err == nil || !strings.Contains(err.Error(), "acme, globex") {
		t.Fatalf("several orgs: %v", err)
	}
	if org, _ := e.ResolveOrg("globex"); org != "globex" {
		t.Fatalf("flag ignored: %q", org)
	}
}

func TestCreateSuiteExplainsAMissingRepo(t *testing.T) {
	e, _ := env(t, oneOrg)
	err := e.CreateSuite("acme", "rspec", "acme/other", "rspec")
	if err == nil || !strings.Contains(err.Error(), "http://x/acme/github/install") {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigureSetsSelectionAndSaysWhatChanged(t *testing.T) {
	e, out := env(t, oneOrg)
	if err := e.Configure("acme", "rspec", api.SuiteSettings{SelectionMode: "shadow"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, `Selection for "RSpec": off → shadow`) {
		t.Fatalf("stdout = %q", got)
	}
}

func TestConfigureSetsDefaultBranchSelection(t *testing.T) {
	e, out := env(t, oneOrg)
	selects := true
	if err := e.Configure("acme", "rspec", api.SuiteSettings{SelectionOnDefaultBranch: &selects}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `Default-branch runs for "RSpec": full → select`) || strings.Contains(got, "Selection for") {
		t.Fatalf("stdout = %q", got)
	}
}

func TestConfigureRefusesBeforeCallingTheServer(t *testing.T) {
	e, _ := env(t, oneOrg)
	if err := e.Configure("acme", "rspec", api.SuiteSettings{}); err == nil || !strings.Contains(err.Error(), "--selection") {
		t.Fatalf("empty settings: err = %v", err)
	}
	if err := e.Configure("acme", "rspec", api.SuiteSettings{SelectionMode: "on"}); err == nil || !strings.Contains(err.Error(), "off, shadow, live") {
		t.Fatalf("bad mode: err = %v", err)
	}
}

func codeOf(err error) contract.Code {
	var coded contract.Coded
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func inDirWithoutAGitRepo(t *testing.T) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestLocalRefusalsCarryTheirContractCode(t *testing.T) {
	t.Setenv("SELECTIVE_ORG", "")
	inDirWithoutAGitRepo(t)
	e, _ := env(t, oneOrg)
	several, _ := env(t, `{"organizations":[{"slug":"acme"},{"slug":"globex"}]}`)
	none, _ := env(t, `{"organizations":[]}`)
	terminal, _ := env(t, oneOrg)
	terminal.StdoutIsTerminal = true

	cases := []struct {
		name string
		err  error
		want contract.Code
	}{
		{"configure with nothing to change", e.Configure("acme", "RSpec", api.SuiteSettings{}), contract.UsageError},
		{"configure with a bad mode", e.Configure("acme", "RSpec", api.SuiteSettings{SelectionMode: "on"}), contract.UsageError},
		{"configure a name no suite has", e.Configure("acme", "nope", api.SuiteSettings{SelectionMode: "off"}), contract.SuiteNotFound},
		{"key for a name no suite has", e.Key("acme", "nope", KeyOptions{Stdout: true}), contract.SuiteNotFound},
		{"key without a destination", e.Key("acme", "RSpec", KeyOptions{}), contract.UsageError},
		{"key with two destinations", e.Key("acme", "RSpec", KeyOptions{Stdout: true, GithubSecret: "K"}), contract.UsageError},
		{"key to a terminal", terminal.Key("acme", "RSpec", KeyOptions{Stdout: true}), contract.UsageError},
		{"key for a suite with no repository", e.Key("acme", "Bare", KeyOptions{GithubSecret: "K"}), contract.UsageError},
		{"several organizations", orgErr(several), contract.UsageError},
		{"no organization", orgErr(none), contract.OrganizationNotFound},
		{"missing origin remote", e.CreateSuite("acme", "rspec", "", "rspec"), contract.UsageError},
		{"repo the app can't see", e.CreateSuite("acme", "rspec", "acme/other", "rspec"), contract.RepoNotInstalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(tc.err); got != tc.want {
				t.Errorf("code = %q, want %q (err: %v)", got, tc.want, tc.err)
			}
		})
	}
}

func TestKeyIntoASecretWithoutGhIsAUsageError(t *testing.T) {
	e, _ := env(t, oneOrg)
	e.RunGh = runGh
	t.Setenv("PATH", t.TempDir())

	err := e.Key("acme", "RSpec", KeyOptions{GithubSecret: "K"})

	if got := codeOf(err); got != contract.UsageError {
		t.Errorf("code = %q, want %q (err: %v)", got, contract.UsageError, err)
	}
}

func orgErr(e *Env) error {
	_, err := e.ResolveOrg("")
	return err
}

func TestRepoNotInstalledKeepsTheInstallURL(t *testing.T) {
	e, _ := env(t, oneOrg)
	err := e.CreateSuite("acme", "rspec", "acme/other", "rspec")

	var fielded contract.Fielded
	if !errors.As(err, &fielded) || fielded.ErrorFields()["install_url"] != "http://x/acme/github/install" {
		t.Errorf("fields = %v, want install_url", fielded)
	}
}

func TestConfigurePassesAnIdTheListDoesNotShowToTheServer(t *testing.T) {
	e, _ := env(t, oneOrg)
	err := e.Configure("acme", missingID, api.SuiteSettings{SelectionMode: "off"})

	if got := codeOf(err); got != contract.NotFound {
		t.Errorf("code = %q, want the server's %q for an unmatched PATCH (err: %v)", got, contract.NotFound, err)
	}
}

const missingID = "00000000-0000-0000-0000-000000000000"

func TestConnectWithoutALoginIsUnauthorized(t *testing.T) {
	keyring.MockInit()
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	t.Setenv("SELECTIVE_TOKEN", "")

	_, err := Connect("http://127.0.0.1:1", true)

	if got := codeOf(err); got != contract.Unauthorized {
		t.Errorf("code = %q, want %q (err: %v)", got, contract.Unauthorized, err)
	}
}

func TestEveryJSONWriterCarriesTheContractVersion(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Env) error
	}{
		{"orgs", func(e *Env) error { return e.Orgs() }},
		{"repos", func(e *Env) error { return e.Repos("acme") }},
		{"suites", func(e *Env) error { return e.Suites("acme") }},
		{"suites create", func(e *Env) error { return e.CreateSuite("acme", "New", "acme/web", "") }},
		{"suites configure", func(e *Env) error {
			return e.Configure("acme", "s-1", api.SuiteSettings{SelectionMode: "shadow"})
		}},
		{"suites key", func(e *Env) error {
			e.RunGh = func(string, ...string) error { return nil }
			return e.Key("acme", "s-1", KeyOptions{GithubSecret: "SELECTIVE_API_KEY"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, out := env(t, oneOrg)
			e.JSON = true

			if err := tc.run(e); err != nil {
				t.Fatal(err)
			}

			var doc map[string]any
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatalf("stdout %q is not one JSON object: %v", out.String(), err)
			}
			if doc["v"] != float64(1) || strings.Count(out.String(), "\n") != 1 {
				t.Errorf("stdout = %q, want one line carrying \"v\":1", out.String())
			}
		})
	}
}

func TestEveryJSONListEndsWithANullNextCursor(t *testing.T) {
	cases := []struct {
		name string
		run  func(*Env) error
	}{
		{"orgs", func(e *Env) error { return e.Orgs() }},
		{"repos", func(e *Env) error { return e.Repos("acme") }},
		{"suites", func(e *Env) error { return e.Suites("acme") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, out := env(t, oneOrg)
			e.JSON = true

			if err := tc.run(e); err != nil {
				t.Fatal(err)
			}

			var doc map[string]any
			if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			cursor, present := doc["next_cursor"]
			if !present || cursor != nil {
				t.Errorf("stdout = %q, want next_cursor present and null", out.String())
			}
		})
	}
}
