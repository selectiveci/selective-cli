package schema

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func fixture(use string, annotations map[string]string) *cobra.Command {
	root := &cobra.Command{Use: "tool"}
	root.PersistentFlags().Bool("json", false, "JSON output")
	leaf := &cobra.Command{Use: use, Short: "s", Annotations: annotations, Run: func(*cobra.Command, []string) {}}
	leaf.Flags().String("name", "", "the name")
	leaf.Flags().Duration("wait", 0, "wait")
	leaf.Flags().String("stdout", "", "print the secret")
	leaf.Flags().String("internal", "", "hidden")
	_ = leaf.MarkFlagRequired("name")
	_ = leaf.Flags().MarkHidden("internal")
	root.AddCommand(leaf)
	return root
}

func TestBuildDescribesPositionalsFlagsAndAnnotations(t *testing.T) {
	root := fixture("put KEY [LABEL] FILES...", map[string]string{
		"exit_codes":   "0,3,4",
		"events":       "summary,heartbeat",
		"mutates":      "true",
		"destructive":  "true",
		"wait_default": "10m",
		"secret_flags": "stdout",
		"mcp":          "skip",
	})
	doc, err := Build(root, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	node, ok := Lookup(doc, []string{"put"})
	if !ok {
		t.Fatalf("put missing from %+v", doc.Commands)
	}
	wantArgs := []Arg{{"KEY", true, false}, {"LABEL", false, false}, {"FILES", true, true}}
	if len(node.Args) != 3 || node.Args[0] != wantArgs[0] || node.Args[1] != wantArgs[1] || node.Args[2] != wantArgs[2] {
		t.Errorf("args = %+v, want %+v", node.Args, wantArgs)
	}
	names := map[string]Flag{}
	for _, f := range node.Flags {
		names[f.Name] = f
	}
	if _, hidden := names["internal"]; hidden {
		t.Error("hidden flag is listed")
	}
	if !names["name"].Required || names["stdout"].Required {
		t.Errorf("required flags wrong: %+v", names)
	}
	if !names["json"].Persistent || names["name"].Persistent {
		t.Errorf("persistent flags wrong: %+v", names)
	}
	if !node.Mutates || !node.Destructive || node.WaitDefault != "10m" || node.MCP != "skip" ||
		strings.Join(node.Events, ",") != "summary,heartbeat" || strings.Join(node.SecretFlags, ",") != "stdout" ||
		len(node.ExitCodes) != 3 || node.ExitCodes[2] != 4 {
		t.Errorf("annotations not carried: %+v", node)
	}
}

func TestBuildLeavesAbsentAnnotationsAbsent(t *testing.T) {
	doc, err := Build(fixture("plain", nil), "v")
	if err != nil {
		t.Fatal(err)
	}
	node, _ := Lookup(doc, []string{"plain"})
	if node.ExitCodes != nil || node.Events != nil || node.Mutates || node.WaitDefault != "" {
		t.Errorf("absent annotations were defaulted: %+v", node)
	}
}

func TestBuildRejectsAnnotationsOutsideTheRegistry(t *testing.T) {
	cases := map[string]map[string]string{
		"unregistered exit":      {"exit_codes": "0,42"},
		"non-numeric exit":       {"exit_codes": "zero"},
		"unregistered event":     {"events": "made_up"},
		"mutates not true":       {"mutates": "yes"},
		"destructive not true":   {"destructive": "1"},
		"wait not a duration":    {"wait_default": "soon"},
		"secret flag not a flag": {"secret_flags": "nope"},
		"mcp not skip":           {"mcp": "hide"},
	}
	for name, annotations := range cases {
		if _, err := Build(fixture("put", annotations), "v"); err == nil {
			t.Errorf("%s: Build accepted %v", name, annotations)
		}
	}
}

func TestBuildRejectsWaitDefaultWithoutAWaitFlag(t *testing.T) {
	root := &cobra.Command{Use: "tool"}
	root.AddCommand(&cobra.Command{Use: "go", Annotations: map[string]string{"wait_default": "1m"}, Run: func(*cobra.Command, []string) {}})
	if _, err := Build(root, "v"); err == nil {
		t.Error("wait_default without --wait was accepted")
	}
}
