package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/schema"
	"github.com/spf13/cobra"
)

type helpOutput struct {
	V    int `json:"v"`
	Help struct {
		Binary   string `json:"binary"`
		Version  string `json:"version"`
		Commands []struct {
			Path  string `json:"path"`
			Flags []struct {
				Name string `json:"name"`
			} `json:"flags"`
		} `json:"commands"`
		Errors []struct {
			Code string `json:"code"`
			Exit int    `json:"exit"`
		} `json:"errors"`
	} `json:"help"`
}

func helpDocument(t *testing.T, args ...string) helpOutput {
	t.Helper()
	code, stdout, stderr := execute(append([]string{"help"}, args...)...)
	if code != 0 {
		t.Fatalf("help exited %d: %s %s", code, stdout, stderr)
	}
	var out helpOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("help output is not one JSON object: %v\n%s", err, stdout)
	}
	return out
}

func TestHelpJSONListsTheCommandTree(t *testing.T) {
	out := helpDocument(t, "--json")
	if out.V != 1 || out.Help.Binary != "selective" {
		t.Errorf("envelope = v%d %q", out.V, out.Help.Binary)
	}
	flags := map[string][]string{}
	for _, c := range out.Help.Commands {
		for _, f := range c.Flags {
			flags[c.Path] = append(flags[c.Path], f.Name)
		}
		if _, seen := flags[c.Path]; !seen {
			flags[c.Path] = nil
		}
	}
	for _, path := range []string{"auth", "auth login", "auth status", "auth logout", "tail", "version", "help"} {
		if _, ok := flags[path]; !ok {
			t.Errorf("%q missing from help --json", path)
		}
	}
	joined := strings.Join(flags["tail"], " ")
	for _, want := range []string{"run-id", "commit", "format", "json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tail flags %q lack --%s", joined, want)
		}
	}
	if strings.Contains(strings.Join(flags["auth"], " "), "token") {
		t.Errorf("auth's hidden --token is listed: %v", flags["auth"])
	}
}

func TestHelpJSONListsEveryRegisteredErrorCode(t *testing.T) {
	out := helpDocument(t, "--json")
	got := map[string]int{}
	for _, e := range out.Help.Errors {
		got[e.Code] = e.Exit
	}
	for _, code := range contract.Codes() {
		if exit, ok := got[string(code)]; !ok || exit != contract.ExitFor(code, 0) {
			t.Errorf("registered code %q: help has exit %d (present=%v), want %d", code, exit, ok, contract.ExitFor(code, 0))
		}
	}
	if len(got) != len(contract.Codes()) {
		t.Errorf("help lists %d error codes, contract registers %d", len(got), len(contract.Codes()))
	}
}

func TestHelpJSONForOneCommand(t *testing.T) {
	code, stdout, _ := execute("help", "auth", "login", "--json")
	var out struct {
		V       int `json:"v"`
		Command struct {
			Path string `json:"path"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || code != 0 {
		t.Fatalf("exit %d, %v: %s", code, err, stdout)
	}
	if out.V != 1 || out.Command.Path != "auth login" {
		t.Errorf("got %+v", out)
	}
}

func TestHelpForAnUnknownCommandIsAUsageError(t *testing.T) {
	code, stdout, _ := execute("help", "nope", "--json")
	if code != contract.ExitUsage || !strings.Contains(stdout, `"error":"usage_error"`) {
		t.Errorf("exit %d, stdout %q", code, stdout)
	}
}

func TestFindWalksNestedCommands(t *testing.T) {
	got, err := find(newRoot(), []string{"auth", "login"})
	if err != nil || got.Name() != "login" {
		t.Errorf("find = %v, %v", got, err)
	}
	if _, err := find(newRoot(), []string{"auth", "nope"}); err == nil {
		t.Error("find accepted an unknown subcommand")
	}
}

func arityAgrees(t *testing.T, cmd *cobra.Command, args []schema.Arg) bool {
	t.Helper()
	if cmd.Args == nil {
		return true
	}
	required, variadic := 0, false
	for _, a := range args {
		if a.Required {
			required++
		}
		variadic = variadic || a.Variadic
	}
	for n := 0; n <= 3; n++ {
		accepted := cmd.Args(cmd, make([]string, n)) == nil
		declared := n >= required && (variadic || n <= len(args))
		if accepted != declared {
			return false
		}
	}
	return true
}

func TestUseAgreesWithArgsOnTheRealTree(t *testing.T) {
	root := newRoot()
	doc, err := schema.Build(root, version)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range doc.Commands {
		cmd, err := find(root, strings.Fields(node.Path))
		if err != nil {
			t.Fatal(err)
		}
		if !arityAgrees(t, cmd, node.Args) {
			t.Errorf("%q: Use %q disagrees with Args", node.Path, cmd.Use)
		}
	}
}

func TestUseArgsProbeCatchesAMismatch(t *testing.T) {
	cmd := &cobra.Command{Use: "put", Args: cobra.ExactArgs(1)}
	if arityAgrees(t, cmd, nil) {
		t.Error("ExactArgs(1) with no positionals in Use passed the probe")
	}
	if !arityAgrees(t, cmd, []schema.Arg{{Name: "KEY", Required: true}}) {
		t.Error("ExactArgs(1) with KEY in Use failed the probe")
	}
}

func TestHelpSchemaFailureExitsInternal(t *testing.T) {
	root := newRoot()
	root.AddCommand(&cobra.Command{Use: "bad", Annotations: map[string]string{"exit_codes": "42"}, Run: func(*cobra.Command, []string) {}})
	var stdout, stderr bytes.Buffer
	code := run(root, []string{"help", "--json"}, &stdout, &stderr)
	if code != contract.ExitInternal || !strings.Contains(stdout.String(), `"error":"internal_error"`) {
		t.Errorf("exit %d, stdout %q", code, stdout.String())
	}
}
