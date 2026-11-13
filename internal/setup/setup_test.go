package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
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
			_, _ = w.Write([]byte(`{"suites":[{"id":"s-1","name":"RSpec","type":"rspec","repo":"acme/web","last_run_at":null}]}`))
		case r.URL.Path == "/api/v1/orgs/acme/suites" && r.Method == "POST":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":"repo_not_installed","message":"The Selective GitHub App can't see this repository.","install_url":"http://x/acme/github/install"}`))
		case r.URL.Path == "/api/v1/orgs/acme/suites/s-1" && r.Method == "PATCH":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"suite":{"id":"s-1","name":"RSpec","selection_mode":"` + body["selection_mode"] + `"},"previous":{"selection_mode":"off"}}`))
		case r.URL.Path == "/api/v1/orgs/acme/suites/s-1/api_key":
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

func TestConfigureRefusesBeforeCallingTheServer(t *testing.T) {
	e, _ := env(t, oneOrg)
	if err := e.Configure("acme", "rspec", api.SuiteSettings{}); err == nil || !strings.Contains(err.Error(), "--selection") {
		t.Fatalf("empty settings: err = %v", err)
	}
	if err := e.Configure("acme", "rspec", api.SuiteSettings{SelectionMode: "on"}); err == nil || !strings.Contains(err.Error(), "off, shadow, live") {
		t.Fatalf("bad mode: err = %v", err)
	}
}
