package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
			&api.Error{Status: 403, Code: contract.InsufficientScope, Message: "needs write", Scope: "write", Fields: map[string]any{"scope": "write"}},
			6,
			`{"v":1,"error":"insufficient_scope","message":"this login can't do that (it needs the \"write\" scope); run: sel auth login --setup","scope":"write"}` + "\n",
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
	"auth login":    true,
	"auth status":   true,
	"auth logout":   true,
	"auth signup":   true,
	"orgs":          true,
	"repos":         true,
	"suites create": true,
	"suites key":    true,
	"tail":          true,
	"version":       true,
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

func TestNoCommandSetsPersistentPreRun(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.PersistentPreRun != nil || c.PersistentPreRunE != nil {
			t.Errorf("%q sets PersistentPreRun; the root wrapper owns it", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())
}
