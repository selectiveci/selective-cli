package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/cobra"
)

func execute(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(newRoot(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUsageErrorsExitFive(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":       {"--bogus"},
		"unknown subcommand": {"nosuch"},
		"extra argument":     {"auth", "login", "extra"},
		"flag without value": {"auth", "login", "--hostname"},
		"tail bad flag":      {"tail", "--bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := execute(args...)

			if code != contract.ExitUsage {
				t.Errorf("exit = %d, want %d (stderr: %s)", code, contract.ExitUsage, stderr)
			}
		})
	}
}

func TestUsageErrorIsAJSONObjectWhenStdoutIsNotATerminal(t *testing.T) {
	code, stdout, stderr := execute("--bogus")

	want := `{"v":1,"error":"usage_error","message":"unknown flag: --bogus"}` + "\n"
	if code != 5 || stdout != want {
		t.Errorf("exit %d, stdout %q, want exit 5 and %q", code, stdout, want)
	}
	if stderr != "Error: unknown flag: --bogus\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

func rootWith(failure error) *cobra.Command {
	root := newRoot()
	root.AddCommand(&cobra.Command{
		Use:  "boom",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return failure },
	})
	return root
}

func TestRuntimeErrorsExitByCode(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		exit    int
		wantOut string
	}{
		{
			"server says no build",
			&api.Error{Status: 404, Code: contract.NoBuild, Message: "No build for this repository and commit."},
			3,
			`{"v":1,"error":"no_build","message":"No build for this repository and commit."}` + "\n",
		},
		{
			"server says the scope is missing",
			&api.Error{Status: 403, Code: contract.InsufficientScope, Message: "needs write", Fields: map[string]any{"scope": "write"}},
			6,
			`{"v":1,"error":"insufficient_scope","message":"needs write","scope":"write"}` + "\n",
		},
		{
			"cli-originated timeout",
			contract.Errorf(contract.Timeout, "gave up after 5s"),
			8,
			`{"v":1,"error":"timeout","message":"gave up after 5s"}` + "\n",
		},
		{
			"untyped url error from a command",
			fmt.Errorf("browser login: %w", &url.Error{Op: "Post", URL: "https://selective.test/device", Err: errors.New("connection refused")}),
			4,
			`{"v":1,"error":"network_error","message":"browser login: Post \"https://selective.test/device\": connection refused"}` + "\n",
		},
		{
			"untyped net error from a command",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("no route to host")},
			4,
			`{"v":1,"error":"network_error","message":"dial tcp: no route to host"}` + "\n",
		},
		{
			"config file error is not a network error",
			&os.PathError{Op: "open", Path: "/home/u/.config/selective/credentials", Err: syscall.ENOENT},
			9,
			`{"v":1,"error":"internal_error","message":"open /home/u/.config/selective/credentials: no such file or directory"}` + "\n",
		},
		{
			"unclassified error",
			http.ErrAbortHandler,
			9,
			`{"v":1,"error":"internal_error","message":"net/http: abort Handler"}` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := run(rootWith(tc.err), []string{"boom"}, &stdout, &stderr)

			if code != tc.exit || stdout.String() != tc.wantOut {
				t.Errorf("exit %d stdout %q, want exit %d stdout %q", code, stdout.String(), tc.exit, tc.wantOut)
			}
		})
	}
}

func TestRejectedTokenExitsFourThroughTheClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","message":"Missing, invalid or revoked API token."}`))
	}))
	t.Cleanup(srv.Close)

	res := runBinary(t, t.TempDir(), srv.URL, "auth", "status")

	if res.exit != 4 || !strings.Contains(res.stdout, "rejected") {
		t.Errorf("exit %d stdout %q, want exit 4 and the rejected-token message", res.exit, res.stdout)
	}
}

func TestDeviceLoginFailuresExitByContractCode(t *testing.T) {
	cases := []struct {
		name        string
		tokenStatus int
		tokenBody   string
		exit        int
		code        string
	}{
		{"person denied the login", 400, `{"error":"access_denied"}`, 6, "approval_denied"},
		{"device code expired", 400, `{"error":"expired_token"}`, 7, "approval_expired"},
		{"server fault while polling", 500, `{"error":"server_error"}`, 4, "server_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/device/code" {
					_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device","expires_in":600,"interval":1}`))
					return
				}
				w.WriteHeader(tc.tokenStatus)
				_, _ = w.Write([]byte(tc.tokenBody))
			}))
			t.Cleanup(srv.Close)

			res := runBinary(t, t.TempDir(), srv.URL, "auth", "login", "--no-browser", "--json")

			lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
			last := lines[len(lines)-1]
			if res.exit != tc.exit || !strings.HasPrefix(last, `{"v":1,"error":"`+tc.code+`"`) {
				t.Errorf("exit %d last line %q, want exit %d and error %q", res.exit, last, tc.exit, tc.code)
			}
		})
	}
}

func TestInterruptingDeviceLoginExitsOneThirty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/device/code" {
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device","expires_in":600,"interval":1}`))
			return
		}
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	t.Cleanup(srv.Close)
	cmd := exec.Command(os.Args[0], "auth", "login", "--no-browser", "--json")
	cmd.Env = append(os.Environ(),
		"SELECTIVE_TEST_RUN_MAIN=1",
		"SELECTIVE_HOST="+srv.URL,
		"SELECTIVE_TOKEN=tok",
		"SELECTIVE_CONFIG_DIR="+t.TempDir(),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	watchdog := time.AfterFunc(20*time.Second, func() { _ = cmd.Process.Kill() })
	defer watchdog.Stop()
	scanner := bufio.NewScanner(stdout)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if strings.Contains(scanner.Text(), `"event":"authorize"`) {
			break
		}
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	_ = cmd.Wait()

	if got := cmd.ProcessState.ExitCode(); got != 130 {
		t.Errorf("exit %d, want 130", got)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, `{"v":1,"event":"`) {
			t.Errorf("event line %q does not start with {\"v\":1,\"event\"", line)
		}
	}
}

func leafPaths(c *cobra.Command, prefix string, out map[string]*cobra.Command) {
	for _, sub := range c.Commands() {
		path := strings.TrimSpace(prefix + " " + sub.Name())
		if sub.HasSubCommands() {
			leafPaths(sub, path, out)
			continue
		}
		out[path] = sub
	}
}

var commandsWithoutExitCodes = map[string]bool{
	"auth login":       true,
	"auth status":      true,
	"auth logout":      true,
	"auth signup":      true,
	"orgs":             true,
	"repos":            true,
	"suites configure": true,
	"suites create":    true,
	"suites key":       true,
	"tail":             true,
	"version":          true,
	"__transport":      true,
}

func TestEveryLeafCommandDeclaresExitCodes(t *testing.T) {
	leaves := map[string]*cobra.Command{}
	leafPaths(newRoot(), "", leaves)

	for path, cmd := range leaves {
		declared := cmd.Annotations["exit_codes"] != ""
		switch {
		case !declared && !commandsWithoutExitCodes[path]:
			t.Errorf("%q has no exit_codes annotation", path)
		case declared && commandsWithoutExitCodes[path]:
			t.Errorf("%q declares exit_codes; remove it from commandsWithoutExitCodes", path)
		}
	}
	for path := range commandsWithoutExitCodes {
		if leaves[path] == nil {
			t.Errorf("commandsWithoutExitCodes lists %q, which is not a command", path)
		}
	}
}

func persistentHookViolations(root *cobra.Command) []string {
	var found []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != root && c.PersistentPreRun != nil {
			found = append(found, c.CommandPath()+" sets PersistentPreRun")
		}
		if c != root && c.PersistentPreRunE != nil {
			found = append(found, c.CommandPath()+" sets PersistentPreRunE")
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return found
}

func TestNoCommandSetsAPersistentPreRunHook(t *testing.T) {
	for _, violation := range persistentHookViolations(newRoot()) {
		t.Error(violation)
	}
}

func TestPersistentHookCheckSeesBothHookFields(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	root.AddCommand(
		&cobra.Command{Use: "a", PersistentPreRun: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "b", PersistentPreRunE: func(*cobra.Command, []string) error { return nil }},
	)

	got := persistentHookViolations(root)

	want := []string{"root a sets PersistentPreRun", "root b sets PersistentPreRunE"}
	if !slices.Equal(got, want) {
		t.Errorf("violations = %v, want %v", got, want)
	}
}

func TestJSONFlagGivenAcceptsBooleanSpellings(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"tail", "--json"}, true},
		{[]string{"tail", "--json=true"}, true},
		{[]string{"tail", "--json=1"}, true},
		{[]string{"tail", "--json=T"}, true},
		{[]string{"tail", "--json=false"}, false},
		{[]string{"tail", "--json=0"}, false},
		{[]string{"tail", "--json=maybe"}, false},
		{[]string{"tail"}, false},
	}
	for _, tc := range cases {
		if got := jsonFlagGiven(tc.args); got != tc.want {
			t.Errorf("jsonFlagGiven(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
