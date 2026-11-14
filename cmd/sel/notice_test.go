package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/update"
)

const orgsBody = `{"organizations":[{"slug":"acme","name":"Acme","role":"owner","github_account":"acme"}]}`

type noticeEnv struct {
	build     string
	cache     string
	env       map[string]string
	stdoutTTY bool
	stderrTTY bool
	status    int
}

func cacheJSON(extra string) string {
	return `{"latest_version":"0.3.0","checked_at":"` + time.Now().UTC().Format(time.RFC3339) + `"` + extra + `}`
}

func outdated() noticeEnv {
	return noticeEnv{build: "0.1.0", cache: cacheJSON(""), stdoutTTY: true, stderrTTY: true, status: 200}
}

type noticeRun struct {
	code           int
	stdout, stderr string
	apiRequests    int32
	cacheWritten   bool
}

func (n noticeEnv) run(t *testing.T, args ...string) noticeRun {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(n.status)
		_, _ = io.WriteString(w, orgsBody)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	t.Setenv("SELECTIVE_CONFIG_DIR", dir)
	t.Setenv("SELECTIVE_HOST", srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "BUILD_NUMBER", "RUN_ID", "CODESPACES", "SELECTIVE_NO_UPDATE_NOTIFIER"} {
		t.Setenv(name, n.env[name])
	}
	if n.cache != "" {
		if err := os.WriteFile(filepath.Join(dir, "update-check.json"), []byte(n.cache), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	isTerminal = func(w io.Writer) bool { return (w == &stdout && n.stdoutTTY) || (w == &stderr && n.stderrTTY) }
	t.Cleanup(func() { isTerminal = formatIsTerminal })
	previous := version
	version = n.build
	t.Cleanup(func() { version = previous })

	captured, err := os.CreateTemp(dir, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = captured
	code := run(newRoot(), args, &stdout, &stderr)
	os.Stdout = realStdout
	written, _ := os.ReadFile(captured.Name())

	_, statErr := os.Stat(filepath.Join(dir, "update-check.json"))
	return noticeRun{code, stdout.String() + string(written), stderr.String(), requests.Load(), statErr == nil}
}

func TestNoticeAppearsOnStderrAfterEveryCommandWhileOutdated(t *testing.T) {
	env := outdated()

	for i := 1; i <= 3; i++ {
		got := env.run(t, "orgs")

		want := "\nA new release of sel is available: 0.1.0 → 0.3.0\nTo upgrade, run: " + update.UpgradeCommand + "\nhttps://github.com/selectiveci/selective-cli/releases/tag/v0.3.0\n"
		if got.code != 0 || got.apiRequests != 1 || !strings.HasSuffix(got.stderr, want) || strings.Contains(got.stdout, "release") {
			t.Fatalf("run %d: exit %d, %d API request(s)\nstdout %q\nstderr %q", i, got.code, got.apiRequests, got.stdout, got.stderr)
		}
	}
}

func TestNoticeIsSilentWhenAGateIsClosed(t *testing.T) {
	cases := map[string]func(*noticeEnv, *[]string){
		"stdout is not a terminal": func(n *noticeEnv, _ *[]string) { n.stdoutTTY = false },
		"stderr is not a terminal": func(n *noticeEnv, _ *[]string) { n.stderrTTY = false },
		"CI":                       func(n *noticeEnv, _ *[]string) { n.env = map[string]string{"CI": "true"} },
		"GITHUB_ACTIONS":           func(n *noticeEnv, _ *[]string) { n.env = map[string]string{"GITHUB_ACTIONS": "true"} },
		"BUILD_NUMBER":             func(n *noticeEnv, _ *[]string) { n.env = map[string]string{"BUILD_NUMBER": "7"} },
		"RUN_ID":                   func(n *noticeEnv, _ *[]string) { n.env = map[string]string{"RUN_ID": "7"} },
		"CODESPACES":               func(n *noticeEnv, _ *[]string) { n.env = map[string]string{"CODESPACES": "true"} },
		"opt-out":                  func(n *noticeEnv, _ *[]string) { n.env = map[string]string{"SELECTIVE_NO_UPDATE_NOTIFIER": "1"} },
		"dev build":                func(n *noticeEnv, _ *[]string) { n.build = "dev" },
		"already current":          func(n *noticeEnv, _ *[]string) { n.build = "0.3.0" },
		"version command":          func(_ *noticeEnv, args *[]string) { *args = []string{"version"} },
		"help command":             func(_ *noticeEnv, args *[]string) { *args = []string{"help"} },
		"--help flag":              func(_ *noticeEnv, args *[]string) { *args = []string{"orgs", "--help"} },
		"bare auth group":          func(_ *noticeEnv, args *[]string) { *args = []string{"auth"} },
		"bare sel":                 func(_ *noticeEnv, args *[]string) { *args = nil },
	}
	for name, close := range cases {
		t.Run(name, func(t *testing.T) {
			env, args := outdated(), []string{"orgs"}
			close(&env, &args)

			got := env.run(t, args...)

			if !got.cacheWritten {
				t.Fatal("the cache the notice would come from is missing, so this silence proves nothing")
			}
			if strings.Contains(got.stderr, "release of sel") || strings.Contains(got.stdout, "release of sel") {
				t.Errorf("notice printed:\nstdout %q\nstderr %q", got.stdout, got.stderr)
			}
		})
	}
}

func TestSkippedCommandsCoverTheContractList(t *testing.T) {
	for _, name := range []string{"version", "help", "completion", "mcp"} {
		if !noNoticeCommands[name] {
			t.Errorf("%q can print the notice", name)
		}
	}
}

func TestStdoutIsByteIdenticalWithTheNoticeOnOrOff(t *testing.T) {
	on := outdated()
	off := outdated()
	off.env = map[string]string{"SELECTIVE_NO_UPDATE_NOTIFIER": "1"}

	shown := on.run(t, "orgs", "--json")
	hidden := off.run(t, "orgs", "--json")

	if shown.stdout == "" || shown.stdout != hidden.stdout || !strings.Contains(shown.stderr, "release of sel") || hidden.stderr != "" {
		t.Errorf("stdout on %q off %q; stderr on %q off %q", shown.stdout, hidden.stdout, shown.stderr, hidden.stderr)
	}
}

func TestBelowTheMinimumSaysNoLongerSupported(t *testing.T) {
	env := outdated()
	env.cache = cacheJSON(`,"server_min_version":"0.2.0"`)

	got := env.run(t, "orgs")

	if !strings.Contains(got.stderr, "sel 0.1.0 is no longer supported: this server needs 0.2.0 or later") || strings.Contains(got.stderr, "A new release") {
		t.Errorf("stderr = %q", got.stderr)
	}
}

func TestNoticeStillPrintsWhenTheCommandFails(t *testing.T) {
	env := outdated()
	env.status = 500

	got := env.run(t, "orgs")

	if got.code == 0 || !strings.Contains(got.stderr, "A new release of sel is available") {
		t.Errorf("exit %d, stderr %q", got.code, got.stderr)
	}
}

func TestAuthSkipsTheNoticeOnlyWhenItJustShowsHelp(t *testing.T) {
	cases := map[string][]string{"bare": nil, "hostname only": {"hostname"}, "legacy token login": {"token"}}
	want := map[string]bool{"bare": true, "hostname only": true, "legacy token login": false}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, _, err := newRoot().Find([]string{"auth"})
			if err != nil {
				t.Fatal(err)
			}
			for _, flag := range set {
				if err := cmd.Flags().Set(flag, "x"); err != nil {
					t.Fatal(err)
				}
			}

			if got := skipsNotice(cmd); got != want[name] {
				t.Errorf("skipsNotice = %v, want %v", got, want[name])
			}
		})
	}
}
