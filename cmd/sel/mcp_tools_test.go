package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/mcpserver"
	"github.com/selectiveci/selective-cli/internal/schema"
	"github.com/spf13/cobra"
)

func listedTools(t *testing.T, root *cobra.Command) []*mcp.Tool {
	t.Helper()
	doc, err := schema.Build(root, version)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	session, err := mcpserver.New(doc, nil).Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var tools []*mcp.Tool
	for tool, err := range client.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, tool)
	}
	return tools
}

func toolNamed(tools []*mcp.Tool, name string) *mcp.Tool {
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

func inputProperties(tool *mcp.Tool) map[string]any {
	return tool.InputSchema.(map[string]any)["properties"].(map[string]any)
}

func helpCommands(t *testing.T) []schema.Command {
	t.Helper()
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help schema.Document `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Help.Commands
}

func TestMCPToolListFollowsTheCommandTree(t *testing.T) {
	tools := listedTools(t, newRoot())

	create := toolNamed(tools, "suites_create")
	if create == nil {
		t.Fatal("suites_create is not listed")
	}
	if _, ok := inputProperties(create)["framework"]; !ok {
		t.Errorf("suites_create properties = %v, want framework", inputProperties(create))
	}
	if got := create.InputSchema.(map[string]any)["required"]; !slices.Contains(got.([]any), any("name")) {
		t.Errorf("suites_create required = %v, want name", got)
	}
	if toolNamed(tools, "test") == nil {
		t.Error("test is not listed")
	}
	for _, name := range []string{"auth", "auth_login", "auth_signup", "auth_logout", "help", "mcp", "__transport"} {
		if toolNamed(tools, name) != nil {
			t.Errorf("%s is listed", name)
		}
	}
	for _, name := range []string{"tokens_create", "suites_key"} {
		tool := toolNamed(tools, name)
		if tool == nil {
			t.Fatalf("%s is not listed", name)
		}
		if _, ok := inputProperties(tool)["stdout"]; ok {
			t.Errorf("%s exposes its secret flag --stdout", name)
		}
	}
	for _, tool := range tools {
		for _, name := range []string{"json", "hostname"} {
			if _, ok := inputProperties(tool)[name]; ok {
				t.Errorf("%s exposes %q", tool.Name, name)
			}
		}
	}
}

func TestMCPToolListHasEachRunnableCommandNotSkippedExactlyOnce(t *testing.T) {
	var want, got []string
	for _, c := range helpCommands(t) {
		if c.Runnable && c.MCP != "skip" && c.Path != "help" && c.Path != "mcp" {
			want = append(want, strings.ReplaceAll(c.Path, " ", "_"))
		}
	}
	for _, tool := range listedTools(t, newRoot()) {
		got = append(got, tool.Name)
	}
	slices.Sort(want)
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

func TestMCPToolsNeverSendDestructiveHintAndMarkReadOnlyOnlyReads(t *testing.T) {
	commands := map[string]schema.Command{}
	for _, c := range helpCommands(t) {
		commands[strings.ReplaceAll(c.Path, " ", "_")] = c
	}

	for _, tool := range listedTools(t, newRoot()) {
		c := commands[tool.Name]
		raw, err := json.Marshal(tool.Annotations)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		readOnly := !c.Mutates && !c.Destructive && !c.LocalExec
		if _, sent := wire["destructiveHint"]; sent {
			t.Errorf("%s sends destructiveHint: %s", tool.Name, raw)
		}
		if (wire["readOnlyHint"] == true) != readOnly {
			t.Errorf("%s readOnlyHint on the wire = %v, want %v: %s", tool.Name, wire["readOnlyHint"] == true, readOnly, raw)
		}
		if (wire["idempotentHint"] == true) != c.Idempotent {
			t.Errorf("%s idempotentHint on the wire = %v, want %v: %s", tool.Name, wire["idempotentHint"] == true, c.Idempotent, raw)
		}
	}
}

func TestMCPSuitesArchiveAndRestoreSendIdempotentHintOnTheWire(t *testing.T) {
	want := map[string]bool{"suites_archive": true, "suites_restore": true}
	for _, tool := range listedTools(t, newRoot()) {
		if !want[tool.Name] {
			continue
		}
		raw, err := json.Marshal(tool.Annotations)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"idempotentHint":true`) {
			t.Errorf("%s wire annotations = %s, want idempotentHint true", tool.Name, raw)
		}
		delete(want, tool.Name)
	}
	for name := range want {
		t.Errorf("%s is not in the tool list", name)
	}
}

func goldenLine(tool *mcp.Tool) string {
	switch a := tool.Annotations; {
	case a == nil:
		return tool.Name + " none"
	case a.ReadOnlyHint:
		return tool.Name + " readOnly"
	case a.IdempotentHint:
		return tool.Name + " idempotent"
	}
	return tool.Name + " confirm"
}

func TestMCPToolListMatchesTheGoldenAllowlist(t *testing.T) {
	raw, err := os.ReadFile("../../internal/mcpserver/testdata/tools.golden")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range listedTools(t, newRoot()) {
		got = append(got, goldenLine(tool))
	}
	slices.Sort(got)

	if want := strings.Split(strings.TrimSpace(string(raw)), "\n"); !slices.Equal(got, want) {
		t.Errorf("tools differ from internal/mcpserver/testdata/tools.golden; edit it by hand if the change is intended\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestOnlyTestIsMarkedLocalExecAndStaysAnOrdinaryCommand(t *testing.T) {
	for _, c := range helpCommands(t) {
		if c.LocalExec != (c.Path == "test") {
			t.Errorf("%s local_exec = %v", c.Path, c.LocalExec)
		}
		if c.Path == "test" && (c.Mutates || c.Destructive) {
			t.Errorf("test mutates = %v destructive = %v, want neither", c.Mutates, c.Destructive)
		}
	}
}

func TestMCPTestToolHasNoReadOnlyHintAndTheDefaultDestructiveHint(t *testing.T) {
	tool := toolNamed(listedTools(t, newRoot()), "test")
	if tool == nil {
		t.Fatal("test is not listed")
	}
	raw, err := json.Marshal(tool.Annotations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "readOnlyHint") || strings.Contains(string(raw), `"destructiveHint":false`) {
		t.Errorf("test annotations on the wire = %s, want no readOnlyHint and the default destructiveHint", raw)
	}
}

func TestOnlyStateSettingCommandsAreIdempotent(t *testing.T) {
	var got []string
	for _, c := range helpCommands(t) {
		if c.Idempotent {
			got = append(got, c.Path)
		}
	}
	slices.Sort(got)

	if want := []string{"flakes mark-fixed", "flakes quarantine", "flakes unquarantine", "suites archive", "suites configure", "suites restore", "tokens revoke"}; !slices.Equal(got, want) {
		t.Errorf("idempotent commands = %v, want %v", got, want)
	}
}

func quotedWords(usage string) []string {
	var words []string
	parts := strings.Split(usage, `"`)
	for i := 1; i+1 < len(parts); i += 2 {
		words = append(words, parts[i])
	}
	return words
}

func sweepValues(f schema.Flag, property map[string]any) []string {
	switch property["type"] {
	case "boolean":
		return []string{""}
	case "integer":
		return []string{"1"}
	}
	if f.Type == "duration" {
		return []string{"1ms"}
	}
	return append([]string{"x"}, quotedWords(f.Usage)...)
}

func sweepArgv(c schema.Command, flag string) []string {
	argv := strings.Fields(c.Path)
	for _, a := range c.Args {
		if a.Required {
			argv = append(argv, "x")
		}
	}
	for _, f := range c.Flags {
		if f.Required && f.Name != flag {
			argv = append(argv, "--"+f.Name+"=x")
		}
	}
	return argv
}

func TestEveryExposedFlagRunsWithTheForcedJSONFlag(t *testing.T) {
	scratch, err := os.MkdirTemp("", "sweep")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	t.Setenv("HOME", scratch)
	t.Setenv("SELECTIVE_CONFIG_DIR", scratch)
	t.Setenv("SELECTIVE_HOST", "127.0.0.1:1")
	t.Setenv("SELECTIVE_TOKEN", "")
	t.Setenv("SELECTIVE_ORG", "")
	t.Setenv("SELECTIVE_NO_UPDATE_NOTIFIER", "1")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	commands := map[string]schema.Command{}
	for _, c := range helpCommands(t) {
		commands[strings.ReplaceAll(c.Path, " ", "_")] = c
	}
	calls := 0
	for _, tool := range listedTools(t, newRoot()) {
		c := commands[tool.Name]
		properties := inputProperties(tool)
		for _, f := range c.Flags {
			property, exposed := properties[strings.ReplaceAll(f.Name, "-", "_")].(map[string]any)
			if !exposed {
				continue
			}
			for _, value := range sweepValues(f, property) {
				setting := "--" + f.Name
				if value != "" {
					setting += "=" + value
				}
				argv := append(sweepArgv(c, f.Name), setting, "--json")
				code, stdout, stderr := execute(argv...)
				calls++
				if out := strings.ToLower(stdout + stderr); code == contract.ExitUsage && strings.Contains(out, "json") {
					t.Errorf("%v: usage error mentions json: %s", argv, out)
				}
			}
		}
	}
	if calls < len(commands) {
		t.Errorf("sweep made %d calls over %d commands", calls, len(commands))
	}
}
