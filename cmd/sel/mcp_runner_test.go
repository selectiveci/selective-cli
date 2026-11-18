package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	return mcpserver.NewRunner(doc, "/bin/sel", mcpserver.Exposure{AllowDestructive: true}), doc
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
		for _, flag := range slices.Concat([]string{"json", "hostname"}, c.SecretFlags, c.SkipFlags) {
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

func TestMCPToolsMakeNoReleaseRequestsWhenAnUpdateCheckIsDue(t *testing.T) {
	var requests atomic.Int32
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(releases.Close)
	scratch := t.TempDir()
	binary := filepath.Join(scratch, "sel")
	pointed := "github.com/selectiveci/selective-cli/internal/update."
	build := exec.Command("go", "build", "-ldflags", "-X main.version=1.0.0 -X "+pointed+"releasesURL="+releases.URL+" -X "+pointed+"releasesAPIURL="+releases.URL, "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	config := filepath.Join(scratch, "config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	seed := func() {
		if err := os.WriteFile(filepath.Join(config, "update-check.json"), []byte(`{"latest_version":"1.0.0","checked_at":"`+stale+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "BUILD_NUMBER", "RUN_ID", "CODESPACES", "SELECTIVE_NO_UPDATE_NOTIFIER", "SELECTIVE_TOKEN", "SELECTIVE_ORG"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", scratch)
	t.Setenv("SELECTIVE_CONFIG_DIR", config)
	t.Setenv("SELECTIVE_HOST", "127.0.0.1:1")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	seed()
	if out, err := exec.Command(binary, "version", "--json").CombinedOutput(); err != nil || requests.Load() == 0 {
		t.Fatalf("control: a due check made %d release requests (err %v): %s", requests.Load(), err, out)
	}
	_, doc := realRunner(t)
	runner := mcpserver.NewRunner(doc, binary, mcpserver.Exposure{AllowDestructive: true})

	for _, tool := range []string{"version", "orgs"} {
		seed()
		before := requests.Load()
		res, err := runner.Run(context.Background(), tool, json.RawMessage(`{}`))
		if err != nil || len(res.Stdout) == 0 {
			t.Fatalf("%s: err = %v, stdout = %q", tool, err, res.Stdout)
		}
		if got := requests.Load() - before; got != 0 {
			t.Errorf("%s made %d release requests, want 0", tool, got)
		}
	}
}
