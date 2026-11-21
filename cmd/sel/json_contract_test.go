package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

func TestOnlyABareGroupWithExplicitJSONLeavesHelpBehind(t *testing.T) {
	host := closedHost(t)
	for _, args := range [][]string{{"auth"}, {"auth", "--json=false"}, {"--json=false"}, {"approvals", "bogus", "--json"}, {"auth", "bogus", "--json"}} {
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
		})
	}
}
