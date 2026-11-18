package main

import (
	"slices"
	"testing"

	"github.com/selectiveci/selective-cli/internal/mcpserver"
)

var (
	readTools        = []string{"approvals_list", "approvals_show", "auth_status", "build", "builds", "doctor", "failures", "flakes_list", "flakes_prompt", "flakes_show", "orgs", "repos", "suites", "suites_show", "tail", "tokens_list", "tokens_show", "trends", "version"}
	writeTools       = []string{"flakes_quarantine", "flakes_unquarantine", "rerun", "suites_archive", "suites_configure", "suites_create", "suites_key", "suites_restore", "suites_settings", "tokens_create"}
	destructiveTools = []string{"flakes_mark-fixed", "suites_delete", "tokens_revoke"}
	localExecTools   = []string{"test"}
)

var modeCases = map[string]struct {
	exposure mcpserver.Exposure
	listed   []string
}{
	"default":                         {mcpserver.Exposure{}, slices.Concat(readTools, writeTools, localExecTools)},
	"allow-destructive":               {mcpserver.Exposure{AllowDestructive: true}, slices.Concat(readTools, writeTools, destructiveTools, localExecTools)},
	"read-only":                       {mcpserver.Exposure{ReadOnly: true}, readTools},
	"read-only and allow-destructive": {mcpserver.Exposure{ReadOnly: true, AllowDestructive: true}, readTools},
}

func TestMCPExposureModesListExactlyThePinnedToolSets(t *testing.T) {
	if len(readTools) != 19 || len(writeTools) != 10 || len(destructiveTools) != 3 || len(localExecTools) != 1 {
		t.Fatalf("pinned sets are %d read, %d write, %d destructive, %d local_exec, want 19, 10, 3, 1", len(readTools), len(writeTools), len(destructiveTools), len(localExecTools))
	}
	for name, c := range modeCases {
		var got []string
		for _, tool := range listedToolsIn(t, newRoot(), c.exposure) {
			got = append(got, tool.Name)
		}
		want := slices.Clone(c.listed)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %d tools %v, want %d tools %v", name, len(got), got, len(want), want)
		}
	}
}
