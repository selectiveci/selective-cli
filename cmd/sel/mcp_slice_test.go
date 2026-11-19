package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/pflag"
)

const cleanElement = "clean"

func validCall(tool *mcp.Tool) map[string]any {
	properties := inputProperties(tool)
	call := map[string]any{}
	for _, required := range tool.InputSchema.(map[string]any)["required"].([]any) {
		name := required.(string)
		switch properties[name].(map[string]any)["type"] {
		case "integer":
			call[name] = json.Number("1")
		case "boolean":
			call[name] = true
		case "array":
			call[name] = []any{"x"}
		default:
			call[name] = "x"
		}
	}
	return call
}

func realParse(t *testing.T, path string, flag *pflag.Flag, flagArgs []string) ([]string, error) {
	t.Helper()
	cmd, _, err := newRoot().Find(strings.Fields(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags(flagArgs); err != nil {
		return nil, err
	}
	return cmd.Flags().Lookup(flag.Name).Value.(pflag.SliceValue).GetSlice(), nil
}

func TestMCPListFlagsRoundTripThroughTheCommandsRealParse(t *testing.T) {
	mustReject := []string{"a,b", `a"b`, `"a"`, "a\nb", "a\r\nb", ""}
	mustAccept := []string{"a\rb", " a ", "a\tb", "a;b", `\`}
	probes := slices.Concat(mustReject, mustAccept, []string{"a\r", "\n", "\r", ",", "a,", `""`, " ", "'a'", "#x", "é"})
	paths := map[string]string{}
	for _, c := range helpCommands(t) {
		paths[strings.ReplaceAll(c.Path, " ", "_")] = c.Path
	}
	runner, _ := realRunner(t)
	visited := map[string]int{}
	for _, tool := range listedTools(t, newRoot()) {
		path := paths[tool.Name]
		cmd, _, err := newRoot().Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			kind := f.Value.Type()
			property := strings.ReplaceAll(f.Name, "-", "_")
			if _, exposed := inputProperties(tool)[property]; !exposed || (kind != "stringSlice" && kind != "stringArray") {
				return
			}
			t.Logf("visiting %s --%s (%s)", path, f.Name, kind)
			visited[kind]++
			argvFor := func(elements ...string) ([]string, error) {
				args := validCall(tool)
				items := make([]any, len(elements))
				for i, e := range elements {
					items[i] = e
				}
				args[property] = items
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				return runner.Argv(tool.Name, raw)
			}
			argv, err := argvFor(cleanElement)
			if err != nil || !slices.Contains(argv, "--"+f.Name+"="+cleanElement) {
				t.Fatalf("%s --%s: baseline argv = %q, err = %v, want it to carry the clean element", path, f.Name, argv, err)
			}
			for _, item := range probes {
				alone, parseErr := realParse(t, path, f, []string{"--" + f.Name + "=" + item})
				survives := parseErr == nil && slices.Equal(alone, []string{item})
				argv, err := argvFor(cleanElement, item)
				switch {
				case kind == "stringArray" && err != nil:
					t.Errorf("%s --%s: stringArray rejected %q: %v", path, f.Name, item, err)
				case kind == "stringSlice" && (err == nil) != survives:
					t.Errorf("%s --%s: element %q accepted = %v, real parse gives it back = %v", path, f.Name, item, err == nil, survives)
				case kind == "stringSlice" && slices.Contains(mustReject, item) && !isInvalidParams(err):
					t.Errorf("%s --%s: element %q err = %v, want invalid params", path, f.Name, item, err)
				case kind == "stringSlice" && slices.Contains(mustAccept, item) && err != nil:
					t.Errorf("%s --%s: element %q rejected: %v", path, f.Name, item, err)
				}
				if err != nil {
					continue
				}
				got, parseErr := realParse(t, path, f, argv[1+len(strings.Fields(path)):])
				if parseErr != nil || !slices.Equal(got, []string{cleanElement, item}) {
					t.Errorf("%s --%s: argv %q parses back to %q (%v), want [%q %q]", path, f.Name, argv, got, parseErr, cleanElement, item)
				}
			}
		})
	}
	if visited["stringSlice"] < 1 || visited["stringArray"] < 2 {
		t.Errorf("visited %v, want at least 1 stringSlice and 2 stringArray flags", visited)
	}
}
