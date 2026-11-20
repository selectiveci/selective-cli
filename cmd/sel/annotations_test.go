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
		if interactive && (strings.Join(c.Events, ",") != "authorize,logged_in") {
			t.Errorf("%q: events %v", c.Path, c.Events)
		}
	}
	for _, path := range []string{"auth login", "auth signup", "auth status", "auth logout", "orgs", "repos"} {
		if declared[path] == 0 {
			t.Errorf("%q declares no exit_codes in help --json", path)
		}
	}
}

func commandDoc(t *testing.T, path ...string) (doc struct {
	Command struct {
		Mutates bool `json:"mutates"`
	} `json:"command"`
}) {
	t.Helper()
	_, stdout, _ := execute(append(append([]string{"help"}, path...), "--json")...)
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSuitesWritesDeclareMutates(t *testing.T) {
	for _, path := range [][]string{{"suites", "create"}, {"suites", "configure"}, {"suites", "key"}} {
		if !commandDoc(t, path...).Command.Mutates {
			t.Errorf("%v does not declare mutates", path)
		}
	}
}
