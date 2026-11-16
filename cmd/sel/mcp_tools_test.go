package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
