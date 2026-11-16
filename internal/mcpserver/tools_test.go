package mcpserver

import (
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/schema"
	"github.com/spf13/cobra"
)

func leaf(use string, annotations map[string]string) *cobra.Command {
	return &cobra.Command{Use: use, Short: use, Annotations: annotations, Run: func(*cobra.Command, []string) {}}
}

func fixtureDocument(t *testing.T) schema.Document {
	t.Helper()
	root := &cobra.Command{Use: "tool"}
	root.PersistentFlags().Bool("json", false, "JSON output")
	run := leaf("run ID [MORE...]", map[string]string{"mutates": "true", "secret_flags": "stdout"})
	run.Flags().Bool("fast", false, "go fast")
	run.Flags().Int("retries", 0, "retries")
	run.Flags().Duration("wait", 0, "wait")
	run.Flags().StringSlice("tag", nil, "tags")
	run.Flags().StringArray("env", nil, "env")
	run.Flags().Bool("dry-run", false, "dry run")
	run.Flags().String("hostname", "", "host")
	run.Flags().String("stdout", "", "secret")
	run.Flags().String("name", "", "name")
	_ = run.MarkFlagRequired("name")
	group := &cobra.Command{Use: "group"}
	group.AddCommand(leaf("child", nil))
	root.AddCommand(run, leaf("login", map[string]string{"mcp": "skip"}), leaf("help", nil), leaf("mcp", nil), group)
	doc, err := schema.Build(root, "1")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func listed(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	tools := map[string]*mcp.Tool{}
	for _, tool := range Tools(fixtureDocument(t)) {
		tools[tool.Name] = tool
	}
	return tools
}

func schemaOf(tool *mcp.Tool) map[string]any {
	return tool.InputSchema.(map[string]any)
}

func properties(tool *mcp.Tool) map[string]map[string]any {
	return schemaOf(tool)["properties"].(map[string]map[string]any)
}

func TestToolsMapFlagAndPositionalTypesAndRequiredness(t *testing.T) {
	tool := listed(t)["run"]
	if tool == nil {
		t.Fatal("run is not listed")
	}
	props := properties(tool)

	want := map[string]string{"fast": "boolean", "retries": "integer", "wait": "string", "tag": "array", "env": "array", "dry_run": "boolean", "name": "string", "id": "string", "more": "array"}
	for name, typ := range want {
		if props[name]["type"] != typ {
			t.Errorf("property %q = %v, want type %s", name, props[name], typ)
		}
	}
	if got := schemaOf(tool)["required"].([]string); !slices.Equal(got, []string{"id", "name"}) {
		t.Errorf("required = %v, want [id name]", got)
	}
}

func TestToolsLeaveOutWhatMustNotBeExposed(t *testing.T) {
	tools := listed(t)
	if tools["run"] == nil {
		t.Fatal("run is not listed")
	}

	for _, name := range []string{"login", "help", "mcp", "group"} {
		if tools[name] != nil {
			t.Errorf("tool %q is listed", name)
		}
	}
	for _, name := range []string{"json", "hostname", "stdout"} {
		if _, ok := properties(tools["run"])[name]; ok {
			t.Errorf("run exposes %q", name)
		}
	}
	if tools["group_child"] == nil {
		t.Error("group_child is not listed")
	}
}

func TestHintsComeOnlyFromTheMutatesAndDestructiveAnnotations(t *testing.T) {
	no, yes := false, true
	cases := map[string]struct {
		command schema.Command
		want    mcp.ToolAnnotations
	}{
		"read":                    {schema.Command{}, mcp.ToolAnnotations{ReadOnlyHint: true}},
		"read with exit codes":    {schema.Command{ExitCodes: []int{0}}, mcp.ToolAnnotations{ReadOnlyHint: true}},
		"mutates":                 {schema.Command{Mutates: true}, mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}},
		"mutates and destructive": {schema.Command{Mutates: true, Destructive: true}, mcp.ToolAnnotations{DestructiveHint: &yes, IdempotentHint: true}},
	}
	for name, c := range cases {
		got := hints(c.command)
		if got.ReadOnlyHint != c.want.ReadOnlyHint || got.IdempotentHint != c.want.IdempotentHint || !reflect.DeepEqual(got.DestructiveHint, c.want.DestructiveHint) {
			t.Errorf("%s: hints = %+v, want %+v", name, got, c.want)
		}
	}
}
