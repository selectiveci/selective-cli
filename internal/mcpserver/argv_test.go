package mcpserver

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
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
		"an explicit false reaches the command, for either default": {
			"run",
			`{"id":"7","name":"n","fast":false,"color":false,"tag":[]}`,
			[]string{"/bin/sel", "run", "--json", "--color=false", "--fast=false", "--name=n", "7"},
		},
		"booleans left out stay out": {
			"run",
			`{"id":"7","name":"n"}`,
			[]string{"/bin/sel", "run", "--json", "--name=n", "7"},
		},
		"integral numbers in any encoding": {
			"run",
			`{"id":"7","name":"n","retries":6e1}`,
			[]string{"/bin/sel", "run", "--json", "--name=n", "--retries=60", "7"},
		},
		"an integral number with a fraction part of zero": {
			"run",
			`{"id":"7","name":"n","retries":60.0}`,
			[]string{"/bin/sel", "run", "--json", "--name=n", "--retries=60", "7"},
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
		"a missing required flag":              {"run", `{"id":"7"}`},
		"a missing required positional":        {"run", `{"name":"n"}`},
		"an integer out of range":              {"run", `{"id":"7","name":"n","retries":1e30}`},
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
	valid := map[string]time.Duration{`{"timeout_s":60.0}`: time.Minute, `{"timeout_s":6e1}`: time.Minute, `{}`: 60 * time.Second, `{"timeout_s":1}`: time.Second, `{"timeout_s":600}`: 600 * time.Second, `{"timeout_s":5}`: 5 * time.Second}
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

func TestMissingRequiredInputsNameWhatIsMissing(t *testing.T) {
	_, err := fixtureRunner(t).Argv("run", json.RawMessage(`{}`))

	if err == nil || !strings.Contains(err.Error(), "id") || !strings.Contains(err.Error(), "name") {
		t.Errorf("err = %v, want it to name id and name", err)
	}
}

func TestEveryToolForbidsAdditionalProperties(t *testing.T) {
	for name, tool := range listed(t) {
		if schemaOf(tool)["additionalProperties"] != false {
			t.Errorf("%s schema = %v, want additionalProperties false", name, schemaOf(tool))
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

func TestIntegerInputsAreJudgedOnTheExactLexeme(t *testing.T) {
	runner := fixtureRunner(t)
	maxInt := strconv.Itoa(math.MaxInt)
	minInt := strconv.Itoa(math.MinInt)
	accepted := map[string]string{
		"60":                  "60",
		"60.0":                "60",
		"6e1":                 "60",
		"600e-1":              "60",
		"0.6e2":               "60",
		"-0":                  "0",
		"0e1000000000":        "0",
		"-3":                  "-3",
		"9007199254740993":    "9007199254740993",
		"90071992547409.93e2": "9007199254740993",
		maxInt:                maxInt,
		minInt:                minInt,
	}
	for lexeme, want := range accepted {
		got, err := runner.Argv("run", json.RawMessage(`{"id":"7","name":"n","retries":`+lexeme+`}`))
		if err != nil || !slices.Contains(got, "--retries="+want) {
			t.Errorf("%s: argv = %q, err = %v, want --retries=%s", lexeme, got, err, want)
		}
	}
	rejected := []string{
		"1.0000000000000001",
		"60.5",
		"1e-1000000000",
		"1e1000000000",
		"1e19",
		"9223372036854775808",
		"-9223372036854775809",
		"92233720368547758080e-1",
		"0.1e0",
		strings.Repeat("9", 100000),
		"1" + strings.Repeat("0", 100000),
		"0." + strings.Repeat("0", 100000) + "1",
	}
	for _, lexeme := range rejected {
		started := time.Now()
		got, err := runner.Argv("run", json.RawMessage(`{"id":"7","name":"n","retries":`+lexeme+`}`))
		if !isInvalid(err) {
			t.Errorf("%.40s: argv = %q, err = %v, want an invalid-params error", lexeme, got, err)
		}
		if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
			t.Errorf("%.40s: decided in %v, want under 50ms", lexeme, elapsed)
		}
	}
}

func TestTimeoutSecondsIsJudgedOnTheExactLexeme(t *testing.T) {
	runner := fixtureRunner(t)
	for _, lexeme := range []string{"600.0000000000001", "600.00000000000001", "1.00000000000000001", "0.99999999999999999", "1e1000000000"} {
		_, _, err := runner.prepare("group_child", json.RawMessage(`{"timeout_s":`+lexeme+`}`))
		if !isInvalid(err) {
			t.Errorf("%s: err = %v, want an invalid-params error", lexeme, err)
		}
	}
}

func isInvalid(err error) bool {
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == jsonrpc.CodeInvalidParams
}
