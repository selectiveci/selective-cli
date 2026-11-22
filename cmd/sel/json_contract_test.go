package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func closedHost(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	host := srv.URL
	srv.Close()
	return host
}

func stdoutObjects(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var objects []map[string]any
	for {
		var obj map[string]any
		err := dec.Decode(&obj)
		if err == io.EOF {
			return objects
		}
		if err != nil {
			t.Fatalf("stdout is not JSON (%v): %q", err, stdout)
		}
		objects = append(objects, obj)
	}
}

func errorObjects(objects []map[string]any) []map[string]any {
	var found []map[string]any
	for _, obj := range objects {
		if _, ok := obj["error"].(string); ok {
			found = append(found, obj)
		}
	}
	return found
}

func availableCommands(t *testing.T, help string) []string {
	t.Helper()
	_, block, ok := strings.Cut(help, "Available Commands:\n")
	if !ok {
		t.Fatalf("no Available Commands block in %q", help)
	}
	block, _, _ = strings.Cut(block, "\n\n")
	var names []string
	for _, line := range strings.Split(block, "\n") {
		names = append(names, strings.Fields(line)[0])
	}
	return names
}

func TestBareGroupWithExplicitJSONIsOneUsageErrorListingTheSubcommands(t *testing.T) {
	host := closedHost(t)
	for _, group := range [][]string{{"approvals"}, {"auth"}, {"flakes"}, {"tokens"}, {}} {
		t.Run(strings.Join(append([]string{"sel"}, group...), " "), func(t *testing.T) {
			help := runBinary(t, t.TempDir(), host, group...)
			want := availableCommands(t, help.stdout)

			res := runBinary(t, t.TempDir(), host, append(slices.Clone(group), "--json")...)

			objects := stdoutObjects(t, res.stdout)
			if res.exit != 5 || len(objects) != 1 || objects[0]["error"] != "usage_error" {
				t.Fatalf("exit %d stdout %q, want exit 5 and one usage_error", res.exit, res.stdout)
			}
			var got []string
			for _, name := range objects[0]["subcommands"].([]any) {
				got = append(got, name.(string))
			}
			if !slices.Equal(got, want) {
				t.Errorf("subcommands = %v, want %v", got, want)
			}
		})
	}
}

func TestUnknownCommandWithExplicitJSONIsOneUsageErrorWithSuggestions(t *testing.T) {
	host := closedHost(t)
	cases := []struct {
		args        []string
		suggestions []string
		groupLevel  bool
	}{
		{[]string{"auth", "logn"}, []string{"login"}, true},
		{[]string{"flakes", "lst"}, []string{"list"}, true},
		{[]string{"tokens", "lisst"}, []string{"list"}, true},
		{[]string{"approvals", "shw"}, []string{"show"}, true},
		{[]string{"auth", "bogus"}, []string{}, true},
		{[]string{"approvals", "bogus"}, []string{}, true},
		{[]string{"flakes", "bogus"}, []string{}, true},
		{[]string{"tokens", "bogus"}, []string{}, true},
		{[]string{"suites", "setings"}, []string{"settings"}, true},
		{[]string{"suites", "bogus"}, []string{}, true},
		{[]string{"autth"}, []string{"auth"}, false},
		{[]string{"bogus"}, []string{}, false},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			res := runBinary(t, t.TempDir(), host, append(slices.Clone(tc.args), "--json")...)

			objects := stdoutObjects(t, res.stdout)
			if res.exit != 5 || len(objects) != 1 || objects[0]["error"] != "usage_error" {
				t.Fatalf("exit %d stdout %q, want exit 5 and one usage_error", res.exit, res.stdout)
			}
			obj := objects[0]
			if !reflect.DeepEqual(obj["suggestions"], toAny(tc.suggestions)) {
				t.Errorf("suggestions = %v, want %v", obj["suggestions"], tc.suggestions)
			}
			if _, has := obj["subcommands"]; has != tc.groupLevel {
				t.Errorf("subcommands present = %v, want %v", has, tc.groupLevel)
			}
			message := obj["message"].(string)
			if !strings.Contains(message, `unknown command "`+tc.args[len(tc.args)-1]+`" for "sel`) || strings.Contains(message, "Did you mean this?") != (len(tc.suggestions) > 0) {
				t.Errorf("message = %q", message)
			}
		})
	}
}

func toAny(names []string) []any {
	out := []any{}
	for _, n := range names {
		out = append(out, n)
	}
	return out
}

func TestOnlyABareGroupWithExplicitJSONLeavesHelpBehind(t *testing.T) {
	host := closedHost(t)
	for _, args := range [][]string{{"auth"}, {"auth", "--json=false"}, {"--json=false"}, {"auth", "logn"}, {"flakes", "lst"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			res := runBinary(t, t.TempDir(), host, args...)
			if res.exit != 0 || !strings.Contains(res.stdout, "Available Commands:") || strings.HasPrefix(res.stdout, "{") {
				t.Errorf("exit %d stdout %q, want help text and exit 0", res.exit, res.stdout)
			}
		})
	}
}

func TestExplicitHelpOnAGroupIsStillHelp(t *testing.T) {
	res := runBinary(t, t.TempDir(), closedHost(t), "auth", "--help", "--json")
	if res.exit != 0 || !strings.Contains(res.stdout, "Available Commands:") {
		t.Errorf("exit %d stdout %q, want help text and exit 0", res.exit, res.stdout)
	}
}

func TestLoginAgainstAnUnreachableHostPrintsOneErrorObject(t *testing.T) {
	host := closedHost(t)
	for _, command := range []string{"login", "signup"} {
		t.Run(command, func(t *testing.T) {
			res := runBinary(t, t.TempDir(), host, "auth", command, "--json", "--no-browser")

			objects := stdoutObjects(t, res.stdout)
			if res.exit != 4 || len(objects) != 1 || objects[0]["error"] != "network_error" {
				t.Errorf("exit %d stdout %q, want exit 4 and one network_error", res.exit, res.stdout)
			}
		})
	}
}

func TestFailedDeviceLoginPrintsOneErrorObject(t *testing.T) {
	cases := []struct {
		name        string
		tokenStatus int
		tokenBody   string
		exit        int
		code        string
	}{
		{"denied", 400, `{"error":"access_denied"}`, 6, "approval_denied"},
		{"expired", 400, `{"error":"expired_token"}`, 7, "approval_expired"},
		{"server fault", 500, `{"error":"server_error"}`, 4, "server_error"},
	}
	for _, tc := range cases {
		for _, command := range []string{"login", "signup"} {
			t.Run(tc.name+" "+command, func(t *testing.T) {
				var polled atomic.Bool
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/api/v1/device/code" {
						_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device","expires_in":600,"interval":1}`))
						return
					}
					polled.Store(r.URL.Path == "/api/v1/device/token")
					w.WriteHeader(tc.tokenStatus)
					_, _ = w.Write([]byte(tc.tokenBody))
				}))
				t.Cleanup(srv.Close)

				res := runBinary(t, t.TempDir(), srv.URL, "auth", command, "--json", "--no-browser")

				errs := errorObjects(stdoutObjects(t, res.stdout))
				if !polled.Load() || res.exit != tc.exit || len(errs) != 1 || errs[0]["error"] != tc.code || errs[0]["event"] != nil {
					t.Errorf("polled %v, exit %d, stdout %q, want exit %d and one %s error object", polled.Load(), res.exit, res.stdout, tc.exit, tc.code)
				}
			})
		}
	}
}

func visibleCommandPaths(c *cobra.Command, prefix []string) [][]string {
	var paths [][]string
	for _, sub := range c.Commands() {
		if sub.Hidden {
			continue
		}
		path := append(slices.Clone(prefix), sub.Name())
		paths = append(paths, path)
		paths = append(paths, visibleCommandPaths(sub, path)...)
	}
	return paths
}

func TestEveryCommandWithExplicitJSONKeepsTheOneErrorObjectContract(t *testing.T) {
	host := closedHost(t)
	paths := append([][]string{{}}, visibleCommandPaths(newRoot(), nil)...)
	if len(paths) < 40 {
		t.Fatalf("walked %d commands, the tree has more than that", len(paths))
	}
	for _, path := range paths {
		name := strings.Join(append([]string{"sel"}, path...), " ")
		t.Run(name, func(t *testing.T) {
			res := runBinary(t, t.TempDir(), host, append(slices.Clone(path), "--json")...)

			objects := stdoutObjects(t, res.stdout)
			errs := len(errorObjects(objects))
			if res.exit != 0 && errs != 1 || res.exit == 0 && errs != 0 {
				t.Errorf("exit %d with %d error objects on stdout: %q", res.exit, errs, res.stdout)
			}
			t.Logf("%s: exit %d, %d objects, %d error objects", name, res.exit, len(objects), errs)

			if len(visibleCommandPaths(findCommand(t, path), nil)) > 0 {
				res := runBinary(t, t.TempDir(), host, append(slices.Clone(path), "bogus", "--json")...)
				if errs := len(errorObjects(stdoutObjects(t, res.stdout))); res.exit != 5 || errs != 1 {
					t.Errorf("unknown subcommand: exit %d with %d error objects on stdout: %q", res.exit, errs, res.stdout)
				}
			}
		})
	}
}

func findCommand(t *testing.T, path []string) *cobra.Command {
	t.Helper()
	cmd, _, err := newRoot().Find(path)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}
