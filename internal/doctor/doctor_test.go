package doctor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/zalando/go-keyring"
)

const (
	token   = "sk_doctor_secret_7f3a9c"
	meOK    = `{"user_id":"u1","email":"ben@example.com","org_slugs":["acme"]}`
	oneOrg  = `{"organizations":[{"slug":"acme","name":"Acme","role":"owner"}]}`
	twoOrgs = `{"organizations":[{"slug":"acme","name":"Acme","role":"owner"},{"slug":"beta","name":"Beta","role":"member"}]}`
	suites  = `{"suites":[{"id":"s-1","name":"RSpec","type":"rspec","repo":"acme/web","last_run_at":"2026-11-01T12:00:00Z"},{"id":"s-2","name":"Other","type":"pytest","repo":"acme/api","last_run_at":null}]}`
	allDone = "config=ok server=ok auth=ok org=ok suite=ok socket=skip runner=skip cli_version=skip"
)

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func healthy() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/v1/me":               reply(200, meOK),
		"/api/v1/orgs":             reply(200, oneOrg),
		"/api/v1/orgs/acme/suites": reply(200, suites),
	}
}

func with(path string, h http.HandlerFunc) map[string]http.HandlerFunc {
	routes := healthy()
	routes[path] = h
	return routes
}

func serve(t *testing.T, routes map[string]http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[r.URL.Path]; ok {
			h(w, r)
			return
		}
		reply(404, `{"error":"not_found","message":"no such route"}`)(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func isolate(t *testing.T, tok string) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	t.Setenv("SELECTIVE_TOKEN", tok)
	t.Setenv("SELECTIVE_HOST", "")
	t.Setenv("SELECTIVE_ORG", "")
}

func inRepo() (string, error) { return "acme/web", nil }

func run(t *testing.T, host string, opts Options) *Report {
	t.Helper()
	isolate(t, token)
	opts.Hostname = host
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Repo == nil {
		opts.Repo = inRepo
	}
	return Run(opts)
}

func shape(r *Report) string {
	parts := make([]string, len(r.Checks))
	for i, c := range r.Checks {
		parts[i] = c.Name + "=" + string(c.Status)
	}
	return strings.Join(parts, " ")
}

func check(t *testing.T, r *Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %s", name, shape(r))
	return Check{}
}

func coded(t *testing.T, r *Report) contract.Coded {
	t.Helper()
	c, ok := r.Err().(contract.Coded)
	if !ok {
		t.Fatalf("Err() = %v, want a coded error for %s", r.Err(), shape(r))
	}
	return c
}

func exitOf(r *Report) int {
	c, ok := r.Err().(contract.Coded)
	if !ok {
		return 0
	}
	return c.ExitCode()
}

func TestEveryCheckIsListedInOrderWhateverFails(t *testing.T) {
	cases := []struct {
		name   string
		routes map[string]http.HandlerFunc
		opts   Options
		exit   int
		code   contract.Code
		shape  string
	}{
		{
			name:   "all good",
			routes: healthy(),
			shape:  allDone,
		},
		{
			name:   "revoked token",
			routes: with("/api/v1/me", reply(401, `{"error":"unauthorized","message":"Token revoked."}`)),
			exit:   4,
			code:   contract.Unauthorized,
			shape:  "config=ok server=ok auth=fail org=skip suite=skip socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "server error",
			routes: with("/api/v1/me", reply(503, `maintenance`)),
			exit:   4,
			code:   contract.ServerError,
			shape:  "config=ok server=fail auth=skip org=skip suite=skip socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "forbidden token passes its code through",
			routes: with("/api/v1/me", reply(403, `{"error":"forbidden","message":"Nope."}`)),
			exit:   6,
			code:   contract.Forbidden,
			shape:  "config=ok server=ok auth=fail org=skip suite=skip socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "a code the registry lacks exits by its status",
			routes: with("/api/v1/me", reply(403, `{"error":"brand_new_refusal","message":"Nope."}`)),
			exit:   6,
			code:   contract.Forbidden,
			shape:  "config=ok server=ok auth=fail org=skip suite=skip socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "two orgs and no --org",
			routes: with("/api/v1/orgs", reply(200, twoOrgs)),
			exit:   5,
			code:   contract.UsageError,
			shape:  "config=ok server=ok auth=ok org=fail suite=skip socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "unknown --org",
			routes: healthy(),
			opts:   Options{Org: "nope"},
			exit:   3,
			code:   contract.OrganizationNotFound,
			shape:  "config=ok server=ok auth=ok org=fail suite=skip socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "no suite for the repo",
			routes: with("/api/v1/orgs/acme/suites", reply(200, `{"suites":[{"id":"s-2","name":"Other","type":"pytest","repo":"acme/api","last_run_at":null}]}`)),
			exit:   3,
			code:   contract.SuiteNotFound,
			shape:  "config=ok server=ok auth=ok org=ok suite=fail socket=skip runner=skip cli_version=skip",
		},
		{
			name:   "no git remote only warns",
			routes: healthy(),
			opts:   Options{Repo: func() (string, error) { return "", contract.Errorf(contract.NotFound, "no origin remote") }},
			shape:  "config=ok server=ok auth=ok org=ok suite=warn socket=skip runner=skip cli_version=skip",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := run(t, serve(t, tc.routes), tc.opts)

			if got := shape(report); got != tc.shape {
				t.Errorf("checks = %s\nwant     %s", got, tc.shape)
			}
			if got := exitOf(report); got != tc.exit {
				t.Errorf("exit = %d, want %d", got, tc.exit)
			}
			if tc.code != "" {
				err := coded(t, report)
				if err.ErrorCode() != tc.code {
					t.Errorf("error code = %q, want %q", err.ErrorCode(), tc.code)
				}
			}
		})
	}
}

func TestReportStatusSummarisesTheChecks(t *testing.T) {
	pass := run(t, serve(t, healthy()), Options{})
	warn := run(t, serve(t, healthy()), Options{Repo: func() (string, error) { return "", contract.Errorf(contract.NotFound, "none") }})
	fail := run(t, serve(t, with("/api/v1/me", reply(401, `{"error":"unauthorized","message":"x"}`))), Options{})

	if pass.Status != "pass" || warn.Status != "warn" || fail.Status != "fail" {
		t.Errorf("statuses = %q %q %q, want pass warn fail", pass.Status, warn.Status, fail.Status)
	}
}

func TestUnreachableServerFailsServerAndSkipsEverythingAfterIt(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	host := dead.URL
	dead.Close()

	report := run(t, host, Options{})

	if got, want := shape(report), "config=ok server=fail auth=skip org=skip suite=skip socket=skip runner=skip cli_version=skip"; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	if got := check(t, report, "server"); got.Code != contract.NetworkError || exitOf(report) != 4 {
		t.Errorf("server = %+v, exit %d, want network_error and exit 4", got, exitOf(report))
	}
	if got := check(t, report, "auth").Message; !strings.Contains(got, "server") {
		t.Errorf("auth skip message %q doesn't name the failed check", got)
	}
}

func TestServerCheckReportsLatencyWhenAnyAnswerArrives(t *testing.T) {
	routes := with("/api/v1/me", reply(401, `{"error":"unauthorized","message":"x"}`))

	got := check(t, run(t, serve(t, routes), Options{}), "server")

	if got.Status != OK || got.MS == nil {
		t.Errorf("server = %+v, want ok with ms", got)
	}
}

func TestNoCredentialFailsAuthWithTheLoginFixAndSendsNoToken(t *testing.T) {
	var sawAuthorization string
	routes := healthy()
	routes["/api/v1/me"] = func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("Authorization")
		reply(401, `{"error":"unauthorized","message":"Missing token."}`)(w, r)
	}
	host := serve(t, routes)
	isolate(t, "")

	report := Run(Options{Hostname: host, Timeout: time.Minute, Repo: inRepo})

	auth := check(t, report, "auth")
	if auth.Status != Fail || auth.Code != contract.Unauthorized || auth.Fix != "sel auth login --setup" {
		t.Errorf("auth = %+v", auth)
	}
	if check(t, report, "server").Status != OK || sawAuthorization != "" {
		t.Errorf("server = %+v, Authorization %q", check(t, report, "server"), sawAuthorization)
	}
}

func TestInvalidConfigIsAUsageErrorNamingThePath(t *testing.T) {
	isolate(t, token)
	if err := writeFile(config.Path(), "{not json"); err != nil {
		t.Fatal(err)
	}

	report := Run(Options{Timeout: time.Minute, Repo: inRepo})

	cfg := check(t, report, "config")
	if cfg.Status != Fail || cfg.Code != contract.UsageError || !strings.Contains(cfg.Fix, config.Path()) {
		t.Errorf("config = %+v", cfg)
	}
	if got, want := shape(report), "config=fail server=skip auth=skip org=skip suite=skip socket=skip runner=skip cli_version=skip"; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	if exitOf(report) != 5 {
		t.Errorf("exit = %d, want 5", exitOf(report))
	}
}

func TestBadHostnameIsAUsageError(t *testing.T) {
	report := run(t, "ftp://nope", Options{})

	if got := check(t, report, "config"); got.Status != Fail || got.Code != contract.UsageError {
		t.Errorf("config = %+v", got)
	}
}

func TestSuiteCheckReportsEachMatch(t *testing.T) {
	got := check(t, run(t, serve(t, healthy()), Options{}), "suite")

	raw, _ := json.Marshal(got.Detail)
	for _, want := range []string{`"name":"RSpec"`, `"type":"rspec"`, `"last_run_at":"2026-11-01T12:00:00Z"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("detail %s lacks %s", raw, want)
		}
	}
	if strings.Contains(string(raw), "Other") {
		t.Errorf("detail %s lists a suite for another repo", raw)
	}
}

func TestMissingSuiteSaysHowToCreateOne(t *testing.T) {
	routes := with("/api/v1/orgs/acme/suites", reply(200, `{"suites":[]}`))

	got := check(t, run(t, serve(t, routes), Options{}), "suite")

	if got.Fix != "sel suites create --name <name> --framework <type>" {
		t.Errorf("fix = %q", got.Fix)
	}
}

func TestHungServerTimesOutTheRunningCheckAndSkipsTheRest(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	routes := healthy()
	routes["/api/v1/me"] = func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}
	host := serve(t, routes)
	t.Cleanup(func() { close(release) })

	began := time.Now()
	report := run(t, host, Options{Timeout: time.Second})
	elapsed := time.Since(began)

	select {
	case <-started:
	default:
		t.Fatal("the handler never received the request, so nothing was hanging")
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("took %s with a 1s timeout", elapsed)
	}
	if got, want := shape(report), "config=ok server=fail auth=skip org=skip suite=skip socket=skip runner=skip cli_version=skip"; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	if got := check(t, report, "server"); got.Code != contract.Timeout || exitOf(report) != 8 {
		t.Errorf("server = %+v, exit %d, want timeout and exit 8", got, exitOf(report))
	}
}

func TestFailureErrorCarriesTheFirstFailedCheckAndEveryCheck(t *testing.T) {
	report := run(t, serve(t, with("/api/v1/me", reply(401, `{"error":"unauthorized","message":"Token revoked."}`))), Options{})

	err := coded(t, report)

	if err.ErrorCode() != contract.Unauthorized || err.Error() != check(t, report, "auth").Message {
		t.Errorf("err = %v (%s)", err, err.ErrorCode())
	}
	if doc := err.(contract.Fielded).ErrorFields()["doctor"]; doc != report {
		t.Errorf("doctor field = %v, want the report", doc)
	}
}

func TestTheTokenIsNeverInTheReport(t *testing.T) {
	for name, status := range map[string]int{"green": 200, "revoked": 401, "broken": 500} {
		t.Run(name, func(t *testing.T) {
			routes := healthy()
			if status != 200 {
				routes["/api/v1/me"] = reply(status, `{"error":"unauthorized","message":"nope"}`)
			}
			report := run(t, serve(t, routes), Options{})

			if len(report.Checks) != 8 {
				t.Fatalf("report has %d checks, want 8", len(report.Checks))
			}
			raw, _ := json.Marshal(report)
			text := string(raw)
			if err := report.Err(); err != nil {
				text += err.Error()
			}
			if strings.Contains(text, token) {
				t.Errorf("the token leaked: %s", text)
			}
		})
	}
}

func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
