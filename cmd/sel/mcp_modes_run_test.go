package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/selectiveci/selective-cli/internal/mcpserver"
	"github.com/selectiveci/selective-cli/internal/schema"
)

func markerRunner(t *testing.T, exposure mcpserver.Exposure) (*mcpserver.Runner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake child is a shell script")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	exe := filepath.Join(dir, "sel")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n: > \"$MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	doc, err := schema.Build(newRoot(), version)
	if err != nil {
		t.Fatal(err)
	}
	return mcpserver.NewRunner(doc, exe, exposure), marker
}

func ran(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

var neverListedTools = []string{"help", "mcp", "auth_login"}

func TestMCPHiddenToolsCannotBeCalledAndRunNothingWhileListedOnesDo(t *testing.T) {
	everyTool := slices.Concat(readTools, writeTools, destructiveTools, localExecTools)
	calls := map[string]map[string]any{}
	for _, tool := range listedTools(t, newRoot()) {
		calls[tool.Name] = validCall(tool)
	}
	for name, c := range modeCases {
		runner, marker := markerRunner(t, c.exposure)
		for _, tool := range everyTool {
			raw, err := json.Marshal(calls[tool])
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(context.Background(), tool, raw)
			if slices.Contains(c.listed, tool) {
				if err != nil || !ran(marker) {
					t.Errorf("%s: listed %s: err = %v, ran = %v, want it to run", name, tool, err, ran(marker))
				}
			} else if !isInvalidParams(err) || ran(marker) {
				t.Errorf("%s: hidden %s: err = %v, ran = %v, want invalid params and no run", name, tool, err, ran(marker))
			}
			_ = os.Remove(marker)
		}
		for _, tool := range neverListedTools {
			_, err := runner.Run(context.Background(), tool, json.RawMessage(`{}`))
			if !isInvalidParams(err) || ran(marker) {
				t.Errorf("%s: never-listed %s: err = %v, ran = %v, want invalid params and no run", name, tool, err, ran(marker))
			}
			_ = os.Remove(marker)
		}
	}
}

func TestMCPNeverListedToolsAreRealCommandsTheTestCanReach(t *testing.T) {
	doc, err := schema.Build(newRoot(), version)
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	for _, c := range doc.Commands {
		present[c.Path] = c.Runnable
	}
	for _, path := range []string{"help", "auth login"} {
		if !present[path] {
			t.Errorf("%q is not a runnable command in the tree, so hiding it proves nothing", path)
		}
	}
}
