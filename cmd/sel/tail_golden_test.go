package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"nhooyr.io/websocket"
)

func TestMain(m *testing.M) {
	switch wrapped := os.Getenv("SELECTIVE_TEST_WRAP_STDOUT"); {
	case wrapped != "":
		api.SetClock(instantClock{})
		os.Exit(run(newRoot(), os.Args[1:], wrappedStdout{wrapped == "eio"}, os.Stderr))
	case len(os.Args) > 1 && os.Args[1] == "__transport", os.Getenv("SELECTIVE_TEST_RUN_MAIN") == "1":
		api.SetClock(instantClock{})
		if v := os.Getenv("SELECTIVE_TEST_VERSION"); v != "" {
			version = v
		}
		main()
		return
	case os.Getenv("FAKE_RUNNER") == "1":
		os.Exit(fakeRunner())
	}
	os.Exit(m.Run())
}

type wrappedStdout struct{ eio bool }

func (w wrappedStdout) Unwrap() io.Writer { return os.Stdout }

func (w wrappedStdout) Write(p []byte) (int, error) {
	if w.eio {
		return 0, syscall.EIO
	}
	return os.Stdout.Write(p)
}

type tailServer struct {
	frames       []string
	lookupStatus int
	lookupBody   string
}

func (s tailServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/builds/lookup":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.lookupStatus)
			_, _ = w.Write([]byte(s.lookupBody))
		case "/api/v1/tail/websocket":
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			ctx := c.CloseRead(context.Background())
			for _, f := range s.frames {
				if c.Write(ctx, websocket.MessageText, []byte(f)) != nil {
					return
				}
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/acme/widget.git"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

type cliResult struct {
	stdout string
	stderr string
	exit   int
}

func runBinary(t *testing.T, dir, host string, args ...string) cliResult {
	t.Helper()
	return runBinaryIn(t, dir, host, t.TempDir(), args...)
}

func runBinaryIn(t *testing.T, dir, host, configDir string, args ...string) cliResult {
	t.Helper()
	return runBinaryStdin(t, dir, host, configDir, "", args...)
}

func runBinaryStdin(t *testing.T, dir, host, configDir, stdin string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(),
		"SELECTIVE_TEST_RUN_MAIN=1",
		"SELECTIVE_HOST="+host,
		"SELECTIVE_TOKEN=tok",
		"SELECTIVE_CONFIG_DIR="+configDir,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

const (
	frameStart    = `{"event":"build_start","build_id":"b","commit":"abc1234def","suites":[{"id":"s1","name":"rspec"}],"total":10}`
	frameSel      = `{"event":"selection","run_id":"r1","suite":"rspec","outcome":"selected","applied":true,"cause":null}`
	frameProgress = `{"event":"progress","completed":4,"total":10,"percent":40,"failed":1}`
	frameFail     = `{"event":"test_fail","node":"n1","file":"spec/a_spec.rb","line":12,"error":"expected true, got false & <more>","preceding_on_node":[{"file":"spec/b_spec.rb","line":3}]}`
	summaryPass   = `{"event":"summary","passed":10,"failed":0,"flaky":0,"ms":2500}`
	summaryFail   = `{"event":"summary","passed":9,"failed":1,"flaky":0,"ms":2500}`
	summaryFlaky  = `{"event":"summary","passed":10,"failed":0,"flaky":1,"ms":2500}`
)

func withV(frames []string) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = strings.Replace(f, `{"event"`, `{"v":1,"event"`, 1)
	}
	return out
}

func TestTailOutputAndExitCodesAreUnchanged(t *testing.T) {
	passing := []string{frameStart, frameSel, frameProgress, summaryPass}
	failing := []string{frameStart, frameSel, frameFail, frameProgress, summaryFail}
	flaky := []string{frameStart, frameFail, summaryFlaky}

	cases := []struct {
		name   string
		frames []string
		args   []string
		exit   int
	}{
		{"pass ndjson", passing, []string{"--format", "ndjson"}, 0},
		{"pass ndjson with v", withV(passing), []string{"--format", "ndjson"}, 0},
		{"fail ndjson", failing, []string{"--format", "ndjson"}, 1},
		{"fail ndjson with v", withV(failing), []string{"--format", "ndjson"}, 1},
		{"fail pretty", failing, []string{"--format", "pretty"}, 1},
		{"flaky ndjson", flaky, []string{"--format", "ndjson"}, 2},
		{"flaky pretty with v", withV(flaky), []string{"--format", "pretty"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := tailServer{
				frames:       tc.frames,
				lookupStatus: http.StatusOK,
				lookupBody:   `{"build_id":"b","status":"completed","run_count":1,"suite_count":1}`,
			}.start(t)

			res := runBinary(t, gitRepo(t), srv.URL, append([]string{"tail"}, tc.args...)...)

			if res.exit != tc.exit {
				t.Errorf("exit = %d, want %d (stderr: %s)", res.exit, tc.exit, res.stderr)
			}
			golden := filepath.Join("testdata", "tail", strings.ReplaceAll(tc.name, " ", "_")+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(res.stdout), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if res.stdout != string(want) {
				t.Errorf("stdout differs from %s\n got: %q\nwant: %q", golden, res.stdout, want)
			}
		})
	}
}

func TestTailExitCodesWithoutAStream(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		exit   int
		code   string
	}{
		{"no build, legacy body", http.StatusNotFound, `{"error":"no build found for repo/sha"}`, 3, "no_build"},
		{"no build, contract body", http.StatusNotFound, `{"error":"no_build","message":"No build for this repository and commit."}`, 3, "no_build"},
		{"rejected token", http.StatusUnauthorized, `{"error":"unauthorized","message":"Missing, invalid or revoked API token."}`, 4, "unauthorized"},
		{"server fault", http.StatusBadGateway, `<html>bad gateway</html>`, 4, "server_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := tailServer{lookupStatus: tc.status, lookupBody: tc.body}.start(t)

			res := runBinary(t, gitRepo(t), srv.URL, "tail", "--format", "ndjson")

			if res.exit != tc.exit {
				t.Errorf("exit = %d, want %d (stderr: %s)", res.exit, tc.exit, res.stderr)
			}
			if want := `"error":"` + tc.code + `"`; !strings.Contains(res.stdout, want) {
				t.Errorf("stdout = %q, want the error object %s", res.stdout, want)
			}
		})
	}
}

func TestTailWritesItsStreamToTheCommandsOutputWriter(t *testing.T) {
	srv := tailServer{
		frames:       []string{frameStart, summaryPass},
		lookupStatus: http.StatusOK,
		lookupBody:   `{"build_id":"b","status":"completed","run_count":1,"suite_count":1}`,
	}.start(t)
	t.Setenv("SELECTIVE_HOST", srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())

	code, stdout, _ := execute("tail", "--run-id", "r1", "--format", "ndjson")

	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); code != 0 || len(lines) != 2 || !strings.Contains(lines[0], `"event":"build_start"`) {
		t.Errorf("exit %d, stdout %q; want exit 0 and the two stream lines on the command's own writer", code, stdout)
	}
}
