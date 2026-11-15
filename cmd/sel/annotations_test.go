package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelpDeclaresExitCodesAndEventsForTheAuthOrgsAndReposCommands(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path      string   `json:"path"`
				ExitCodes []int    `json:"exit_codes"`
				Events    []string `json:"events"`
				MCP       string   `json:"mcp"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	declared := map[string]int{}
	for _, c := range doc.Help.Commands {
		declared[c.Path] = len(c.ExitCodes)
		interactive := c.Path == "auth login" || c.Path == "auth signup"
		if interactive && (strings.Join(c.Events, ",") != "authorize,logged_in" || c.MCP != "skip") {
			t.Errorf("%q: events %v, mcp %q", c.Path, c.Events, c.MCP)
		}
	}
	for _, path := range []string{"auth login", "auth signup", "auth status", "auth logout", "orgs", "repos"} {
		if declared[path] == 0 {
			t.Errorf("%q declares no exit_codes in help --json", path)
		}
	}
}
