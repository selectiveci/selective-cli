package mcpserver

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

func fixtureRunner(t *testing.T) *Runner {
	t.Helper()
	return NewRunner(fixtureDocument(t), "/bin/sel")
}

func TestArgvOrdersCommandWordsJSONFlagsThenPositionals(t *testing.T) {
	runner := fixtureRunner(t)
	cases := map[string]struct {
		tool string
		args string
		want []string
	}{
		"every kind of value": {
			"run",
			`{"id":"7","more":["a","b"],"name":"n","retries":3,"wait":"5s","fast":true,"dry_run":true,"tag":["x","y"],"env":["E=1"]}`,
			[]string{"/bin/sel", "run", "--json", "--dry-run", "--env=E=1", "--fast", "--name=n", "--retries=3", "--tag=x", "--tag=y", "--wait=5s", "7", "a", "b"},
		},
		"false booleans and empty lists are left out": {
			"run",
			`{"id":"7","name":"n","fast":false,"dry_run":false,"tag":[]}`,
			[]string{"/bin/sel", "run", "--json", "--name=n", "7"},
		},
		"a value that starts with a dash stays attached to its flag": {
			"run",
			`{"id":"7","name":"--hostname=evil"}`,
			[]string{"/bin/sel", "run", "--json", "--name=--hostname=evil", "7"},
		},
		"a nested command contributes every path word": {
			"group_child",
			`{}`,
			[]string{"/bin/sel", "group", "child", "--json"},
		},
		"no arguments at all": {
			"group_child",
			``,
			[]string{"/bin/sel", "group", "child", "--json"},
		},
		"timeout_s never reaches the command": {
			"group_child",
			`{"timeout_s":30}`,
			[]string{"/bin/sel", "group", "child", "--json"},
		},
	}
	for name, c := range cases {
		got, err := runner.Argv(c.tool, json.RawMessage(c.args))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: argv = %q, want %q", name, got, c.want)
		}
	}
}

func TestArgvRejectsWhatTheToolDoesNotExposeAsInvalidParams(t *testing.T) {
	runner := fixtureRunner(t)
	cases := map[string]struct{ tool, args string }{
		"unknown property":                     {"run", `{"id":"7","name":"n","bogus":1}`},
		"hidden-by-never-exposed hostname":     {"run", `{"id":"7","name":"n","hostname":"h"}`},
		"never-exposed json":                   {"run", `{"id":"7","name":"n","json":true}`},
		"cobra-hidden flag":                    {"run", `{"id":"7","name":"n","token":"t"}`},
		"secret flag":                          {"run", `{"id":"7","name":"n","stdout":"/tmp/k"}`},
		"positional that is a flag":            {"run", `{"id":"--stdout=/tmp/k","name":"n"}`},
		"variadic positional that is a flag":   {"run", `{"id":"7","name":"n","more":["a","-x"]}`},
		"a skipped command":                    {"login", `{}`},
		"help":                                 {"help", `{}`},
		"mcp":                                  {"mcp", `{}`},
		"a group that is not runnable":         {"group", `{}`},
		"an unknown tool":                      {"nope", `{}`},
		"arguments that are not an object":     {"run", `["7"]`},
		"arguments with trailing data":         {"run", `{"id":"7","name":"n"} {}`},
		"a non-integral integer":               {"run", `{"id":"7","name":"n","retries":1.5}`},
		"an integer given as a string":         {"run", `{"id":"7","name":"n","retries":"3"}`},
		"a boolean given as a string":          {"run", `{"id":"7","name":"n","fast":"yes"}`},
		"a list given as a string":             {"run", `{"id":"7","name":"n","tag":"x"}`},
		"a list holding a non-string":          {"run", `{"id":"7","name":"n","tag":[1]}`},
		"a variadic positional given a string": {"run", `{"id":"7","name":"n","more":"a"}`},
		"a string given as a number":           {"run", `{"id":7,"name":"n"}`},
	}
	for name, c := range cases {
		got, err := runner.Argv(c.tool, json.RawMessage(c.args))
		var wire *jsonrpc.Error
		if !errors.As(err, &wire) || wire.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("%s: err = %v, argv = %q, want an invalid-params error", name, err, got)
		}
	}
}

func TestTimeoutDefaultsToSixtySecondsAndMustBeBetweenOneAndSixHundred(t *testing.T) {
	runner := fixtureRunner(t)
	valid := map[string]time.Duration{`{}`: 60 * time.Second, `{"timeout_s":1}`: time.Second, `{"timeout_s":600}`: 600 * time.Second, `{"timeout_s":5}`: 5 * time.Second}
	for args, want := range valid {
		_, got, err := runner.prepare("group_child", json.RawMessage(args))
		if err != nil || got != want {
			t.Errorf("%s: timeout = %v, err = %v, want %v", args, got, err, want)
		}
	}
	for _, args := range []string{`{"timeout_s":0}`, `{"timeout_s":-5}`, `{"timeout_s":601}`, `{"timeout_s":1.5}`, `{"timeout_s":"60"}`, `{"timeout_s":null}`} {
		_, _, err := runner.prepare("group_child", json.RawMessage(args))
		var wire *jsonrpc.Error
		if !errors.As(err, &wire) || wire.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("%s: err = %v, want an invalid-params error", args, err)
		}
	}
}

func TestEveryToolAdvertisesTimeoutSeconds(t *testing.T) {
	for name, tool := range listed(t) {
		prop := properties(tool)[timeoutProperty]
		if prop["type"] != "integer" || prop["minimum"] != 1 || prop["maximum"] != 600 || prop["default"] != 60 {
			t.Errorf("%s timeout_s = %v, want integer 1..600 default 60", name, prop)
		}
	}
}
