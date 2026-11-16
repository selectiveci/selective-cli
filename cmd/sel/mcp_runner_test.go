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
		"nested command with a positional":                        {"suites_key", `{"suite":"s1"}`, []string{"/bin/sel", "suites", "key", "--json", "s1"}},
		"int and string flags":                                    {"builds", `{"limit":5}`, []string{"/bin/sel", "builds", "--json", "--limit=5"}},
		"list flags repeat":                                       {"tokens_create", `{"name":"ci","repo":["a","b"],"scope":["read"]}`, []string{"/bin/sel", "tokens", "create", "--json", "--name=ci", "--repo=a", "--repo=b", "--scope=read"}},
		"explicit false on a flag the command tests with Changed": {"builds", `{"flaky":false}`, []string{"/bin/sel", "builds", "--json", "--flaky=false"}},
		"variadic positionals":                                    {"test", `{"path":["spec/a_spec.rb","spec/b_spec.rb"],"timeout":"5m"}`, []string{"/bin/sel", "test", "--json", "--timeout=5m", "spec/a_spec.rb", "spec/b_spec.rb"}},
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
	var exposed []string
	for _, tool := range listedTools(t, newRoot()) {
		exposed = append(exposed, tool.Name)
	}
	for _, c := range doc.Commands {
		tool := strings.ReplaceAll(c.Path, " ", "_")
		if !slices.Contains(exposed, tool) {
			continue
		}
		for _, flag := range append([]string{"json", "hostname"}, c.SecretFlags...) {
			args := `{"` + strings.ReplaceAll(flag, "-", "_") + `":"x"}`
			if flag == "json" {
				args = `{"json":true}`
			}
			if _, err := runner.Argv(tool, json.RawMessage(args)); !isInvalidParams(err) || !strings.Contains(err.Error(), "unknown properties") {
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

func TestMCPRunnerRejectsMissingRequiredInputsOfTheRealTree(t *testing.T) {
	runner, _ := realRunner(t)
	for tool, args := range map[string]string{"tokens_create": `{}`, "suites_key": `{}`} {
		if argv, err := runner.Argv(tool, json.RawMessage(args)); !isInvalidParams(err) {
			t.Errorf("%s: argv = %q err = %v, want invalid params", tool, argv, err)
		}
	}
}

func TestMCPToolsForbidAdditionalProperties(t *testing.T) {
	for _, tool := range listedTools(t, newRoot()) {
		if tool.InputSchema.(map[string]any)["additionalProperties"] != false {
			t.Errorf("%s does not set additionalProperties false", tool.Name)
		}
	}
}

func TestMCPToolsAdvertiseTimeoutSeconds(t *testing.T) {
	for _, tool := range listedTools(t, newRoot()) {
		if _, ok := inputProperties(tool)["timeout_s"]; !ok {
			t.Errorf("%s has no timeout_s", tool.Name)
		}
	}
}

func TestMCPRunnerRejectsIntegerLexemesThatFloatRoundingWouldAccept(t *testing.T) {
	runner, _ := realRunner(t)

	for _, args := range []string{`{"limit":1.0000000000000001}`, `{"limit":9223372036854775808}`, `{"timeout_s":600.00000000000001}`} {
		_, err := runner.Argv("builds", json.RawMessage(args))
		if !isInvalidParams(err) {
			t.Errorf("%s: err = %v, want an invalid-params error", args, err)
		}
	}
	got, err := runner.Argv("builds", json.RawMessage(`{"limit":9007199254740993}`))
	if err != nil || !slices.Contains(got, "--limit=9007199254740993") {
		t.Errorf("argv = %q, err = %v, want --limit=9007199254740993", got, err)
	}
}
