package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/selectiveci/selective-cli/internal/mcpserver"
	"github.com/selectiveci/selective-cli/internal/schema"
)

func realRunner(t *testing.T) (*mcpserver.Runner, schema.Document) {
	t.Helper()
	doc, err := schema.Build(newRoot(), version)
	if err != nil {
		t.Fatal(err)
	}
	return mcpserver.NewRunner(doc, "/bin/sel"), doc
}

func isInvalidParams(err error) bool {
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == jsonrpc.CodeInvalidParams
}

func TestMCPRunnerMapsRealCommandsToTheirArgv(t *testing.T) {
	runner, _ := realRunner(t)
	cases := map[string]struct {
		tool string
		args string
		want []string
	}{
		"nested command with a positional": {"suites_key", `{"suite":"s1"}`, []string{"/bin/sel", "suites", "key", "--json", "s1"}},
		"int and string flags":             {"builds", `{"limit":5}`, []string{"/bin/sel", "builds", "--json", "--limit=5"}},
		"list flags repeat":                {"tokens_create", `{"repo":["a","b"],"scope":["read"]}`, []string{"/bin/sel", "tokens", "create", "--json", "--repo=a", "--repo=b", "--scope=read"}},
		"variadic positionals":             {"test", `{"path":["spec/a_spec.rb","spec/b_spec.rb"],"timeout":"5m"}`, []string{"/bin/sel", "test", "--json", "--timeout=5m", "spec/a_spec.rb", "spec/b_spec.rb"}},
	}
	for name, c := range cases {
		got, err := runner.Argv(c.tool, json.RawMessage(c.args))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: argv = %q, want %q", name, got, c.want)
		}
	}
}

func TestMCPRunnerRejectsEverySecretAndNeverExposedFlagOfTheRealTree(t *testing.T) {
	runner, doc := realRunner(t)
	checked := 0
	for _, c := range doc.Commands {
		tool := strings.ReplaceAll(c.Path, " ", "_")
		for _, flag := range append([]string{"json", "hostname"}, c.SecretFlags...) {
			args := `{"` + strings.ReplaceAll(flag, "-", "_") + `":"x"}`
			if flag == "json" {
				args = `{"json":true}`
			}
			if _, err := runner.Argv(tool, json.RawMessage(args)); !isInvalidParams(err) {
				t.Errorf("%s: err = %v for --%s, want invalid params", tool, err, flag)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("nothing was checked")
	}
	for _, name := range []string{"auth_login", "auth_logout", "help", "mcp", "__transport"} {
		if _, err := runner.Argv(name, json.RawMessage(`{}`)); !isInvalidParams(err) {
			t.Errorf("%s: err = %v, want invalid params", name, err)
		}
	}
	if _, err := runner.Argv("tokens_create", json.RawMessage(`{"stdout":"/tmp/k"}`)); !isInvalidParams(err) {
		t.Errorf("tokens_create accepted its secret flag --stdout: %v", err)
	}
}

func TestMCPToolsAdvertiseTimeoutSeconds(t *testing.T) {
	for _, tool := range listedTools(t, newRoot()) {
		if _, ok := inputProperties(tool)["timeout_s"]; !ok {
			t.Errorf("%s has no timeout_s", tool.Name)
		}
	}
}
