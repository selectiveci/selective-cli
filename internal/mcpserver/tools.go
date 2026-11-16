package mcpserver

import (
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/schema"
)

var neverExposed = []string{"json", "hostname"}

func New(doc schema.Document, call mcp.ToolHandler) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "selective", Version: doc.Version}, nil)
	for _, tool := range Tools(doc) {
		server.AddTool(tool, call)
	}
	return server
}

func Tools(doc schema.Document) []*mcp.Tool {
	var tools []*mcp.Tool
	for _, c := range doc.Commands {
		if exposed(c) {
			tools = append(tools, tool(c))
		}
	}
	return tools
}

func exposed(c schema.Command) bool {
	return c.Runnable && c.MCP != "skip" && c.Path != "help" && c.Path != "mcp"
}

func toolName(path string) string {
	return strings.ReplaceAll(path, " ", "_")
}

func propertyName(flag string) string {
	return strings.ReplaceAll(flag, "-", "_")
}

func tool(c schema.Command) *mcp.Tool {
	properties := map[string]map[string]any{}
	required := []string{}
	for _, f := range c.Flags {
		if slices.Contains(neverExposed, f.Name) || slices.Contains(c.SecretFlags, f.Name) {
			continue
		}
		properties[propertyName(f.Name)] = property(f)
		if f.Required {
			required = append(required, propertyName(f.Name))
		}
	}
	for _, a := range c.Args {
		name := strings.ToLower(a.Name)
		properties[name] = positional(a)
		if a.Required {
			required = append(required, name)
		}
	}
	slices.Sort(required)
	return &mcp.Tool{
		Name:        toolName(c.Path),
		Description: c.Short,
		InputSchema: map[string]any{"type": "object", "properties": properties, "required": required},
		Annotations: hints(c),
	}
}

func property(f schema.Flag) map[string]any {
	p := map[string]any{"description": f.Usage}
	switch f.Type {
	case "bool":
		p["type"] = "boolean"
	case "int":
		p["type"] = "integer"
	case "stringArray", "stringSlice":
		p["type"] = "array"
		p["items"] = map[string]any{"type": "string"}
	default:
		p["type"] = "string"
	}
	return p
}

func positional(a schema.Arg) map[string]any {
	if a.Variadic {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	return map[string]any{"type": "string"}
}

func hints(c schema.Command) *mcp.ToolAnnotations {
	if c.LocalExec {
		return &mcp.ToolAnnotations{}
	}
	if !c.Mutates && !c.Destructive {
		return &mcp.ToolAnnotations{ReadOnlyHint: true}
	}
	destructive := c.Destructive
	return &mcp.ToolAnnotations{DestructiveHint: &destructive, IdempotentHint: c.Mutates}
}
