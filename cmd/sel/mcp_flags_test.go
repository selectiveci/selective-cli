package main

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/selectiveci/selective-cli/internal/mcpserver"
)

func tokensCreateCall(t *testing.T, extra map[string]any) json.RawMessage {
	t.Helper()
	call := validCall(toolNamed(listedTools(t, newRoot()), "tokens_create"))
	maps.Copy(call, extra)
	raw, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMCPTokensCreateOmitsTheFlagsAnAgentMustNotSet(t *testing.T) {
	properties := inputProperties(toolNamed(listedTools(t, newRoot()), "tokens_create"))
	for _, name := range []string{"no_approval", "output"} {
		if _, ok := properties[name]; ok {
			t.Errorf("tokens_create exposes %s", name)
		}
	}
	for _, c := range helpCommands(t) {
		if c.Path == "tokens create" && !slices.Equal(c.SkipFlags, []string{"no-approval", "output"}) {
			t.Errorf("tokens create mcp_skip_flags = %v, want no-approval and output", c.SkipFlags)
		}
	}
}

func TestMCPTokensCreateCallsPassingThoseFlagsAreInvalidParamsAndSpawnNothing(t *testing.T) {
	runner, marker := markerRunner(t, mcpserver.Exposure{AllowDestructive: true})
	if _, err := runner.Run(context.Background(), "tokens_create", tokensCreateCall(t, nil)); err != nil || !ran(marker) {
		t.Fatalf("control: err = %v, ran = %v, want one spawn", err, ran(marker))
	}
	for name, extra := range map[string]map[string]any{
		"no_approval true":  {"no_approval": true},
		"no_approval false": {"no_approval": false},
		"output":            {"output": "/tmp/secret"},
	} {
		_ = os.Remove(marker)
		_, err := runner.Run(context.Background(), "tokens_create", tokensCreateCall(t, extra))
		if !isInvalidParams(err) || ran(marker) {
			t.Errorf("%s: err = %v, ran = %v, want invalid params and no spawn", name, err, ran(marker))
		}
	}
}

func TestMCPListFlagWithItsBadElementFirstIsInvalidParamsAndSpawnsNothing(t *testing.T) {
	runner, marker := markerRunner(t, mcpserver.Exposure{AllowDestructive: true})
	if _, err := runner.Run(context.Background(), "tokens_create", tokensCreateCall(t, map[string]any{"scope": []any{"read", "write"}})); err != nil || !ran(marker) {
		t.Fatalf("control: err = %v, ran = %v, want one spawn", err, ran(marker))
	}
	_ = os.Remove(marker)
	_, err := runner.Run(context.Background(), "tokens_create", tokensCreateCall(t, map[string]any{"scope": []any{"read,write", "x"}}))
	if !isInvalidParams(err) || ran(marker) {
		t.Errorf("err = %v, ran = %v, want invalid params and no spawn", err, ran(marker))
	}
}
