package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/schema"
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

func helpCommands(t *testing.T) []schema.Command {
	t.Helper()
	code, stdout, stderr := execute("help", "--json")
	var doc struct {
		Help schema.Document `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || code != 0 || len(doc.Help.Commands) == 0 {
		t.Fatalf("help --json: exit %d, %d commands, err %v, stderr %q", code, len(doc.Help.Commands), err, stderr)
	}
	return doc.Help.Commands
}

func TestOnlyTestIsMarkedLocalExecAndStaysAnOrdinaryCommand(t *testing.T) {
	for _, c := range helpCommands(t) {
		if c.LocalExec != (c.Path == "test") {
			t.Errorf("%s local_exec = %v", c.Path, c.LocalExec)
		}
		if c.Path == "test" && (c.Mutates || c.Destructive) {
			t.Errorf("test mutates = %v destructive = %v, want neither", c.Mutates, c.Destructive)
		}
	}
}

func TestOnlyStateSettingCommandsAreIdempotent(t *testing.T) {
	var got []string
	for _, c := range helpCommands(t) {
		if c.Idempotent {
			got = append(got, c.Path)
		}
	}
	slices.Sort(got)

	if want := []string{"flakes mark-fixed", "flakes quarantine", "flakes unquarantine", "suites archive", "suites configure", "suites restore", "tokens revoke"}; !slices.Equal(got, want) {
		t.Errorf("idempotent commands = %v, want %v", got, want)
	}
}
