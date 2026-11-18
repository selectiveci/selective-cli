package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/schema"
)

const (
	fakeScript = `#!/bin/sh
[ -n "$FAKE_OUT" ] && cat "$FAKE_OUT"
[ -n "$FAKE_ERR" ] && cat "$FAKE_ERR" >&2
[ -n "$FAKE_HANG" ] && exec sleep 30
exit "${FAKE_EXIT:-0}"
`
	oneShot     = `{"v":1,"orgs":[]}` + "\n"
	progress    = `{"v":1,"event":"progress","done":1}` + "\n"
	summary     = `{"v":1,"event":"summary","passed":3,"failed":1}` + "\n"
	interrupted = `{"v":1,"event":"summary","interrupted":true}` + "\n"
	pending     = `{"v":1,"result":"pending_approval","dry_run":false,"approval":{"id":"a1","status":"pending"}}` + "\n"
)

type child struct {
	out, err string
	exit     int
	hang     bool
}

func errorLine(code string) string {
	return `{"v":1,"error":"` + code + `","message":"m"}` + "\n"
}

func callFake(t *testing.T, c child, timeoutSeconds int) *mcp.CallToolResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake child is a shell script")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "sel")
	if err := os.WriteFile(exe, []byte(fakeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"FAKE_OUT": c.out, "FAKE_ERR": c.err} {
		path := ""
		if body != "" {
			path = filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv(name, path)
	}
	t.Setenv("FAKE_EXIT", strconv.Itoa(c.exit))
	t.Setenv("FAKE_HANG", map[bool]string{true: "1"}[c.hang])
	doc := schema.Document{Commands: []schema.Command{{Path: "fake", Runnable: true}}}
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	server, err := New(doc, Exposure{}, NewRunner(doc, exe, Exposure{}).Handler()).Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "fake", Arguments: map[string]any{timeoutProperty: timeoutSeconds}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func meta(res *mcp.CallToolResult) map[string]any {
	m, _ := res.Meta["selective"].(map[string]any)
	return m
}

func structured(res *mcp.CallToolResult) map[string]any {
	m, _ := res.StructuredContent.(map[string]any)
	return m
}

func eventList(res *mcp.CallToolResult) []map[string]any {
	var out []map[string]any
	list, _ := structured(res)["events"].([]any)
	for _, e := range list {
		out = append(out, e.(map[string]any))
	}
	return out
}

func text(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(*mcp.TextContent).Text
}

var resultCases = []struct {
	name    string
	c       child
	isError bool
}{
	{"exit 0 one-shot", child{out: oneShot}, false},
	{"exit 1 ends with summary", child{out: progress + summary, exit: 1}, false},
	{"exit 2 ends with summary", child{out: progress + summary, exit: 2}, false},
	{"exit 7 ends with the approval", child{out: pending, exit: 7}, false},
	{"exit 3", child{out: errorLine("not_found"), exit: 3}, true},
	{"exit 4", child{out: errorLine("unauthorized"), exit: 4}, true},
	{"exit 5", child{out: errorLine("usage_error"), exit: 5}, true},
	{"exit 6", child{out: errorLine("insufficient_scope"), exit: 6}, true},
	{"exit 8", child{out: errorLine("timeout"), exit: 8}, true},
	{"exit 9", child{out: errorLine("internal_error"), exit: 9}, true},
	{"exit 130 with an interrupted summary", child{out: progress + interrupted, exit: 130}, true},
	{"exit 130 with empty stdout", child{exit: 130}, true},
}

func TestExitCodesMapToToolResultsPinnedAsLiterals(t *testing.T) {
	for _, tc := range resultCases {
		res := callFake(t, tc.c, 5)
		if res.IsError != tc.isError || meta(res)["exit_code"] != float64(tc.c.exit) {
			t.Errorf("%s: isError = %v, exit_code = %v, want %v and %d", tc.name, res.IsError, meta(res)["exit_code"], tc.isError, tc.c.exit)
		}
	}
}

func TestEveryResultCarriesTheMetaIncludingErrorResults(t *testing.T) {
	for _, tc := range resultCases {
		m := meta(callFake(t, tc.c, 5))
		duration, isNumber := m["duration_ms"].(float64)
		if m["command"] != "fake" || m["retryable"] != (tc.c.exit == 4) || !isNumber || duration < 0 {
			t.Errorf("%s: _meta.selective = %v, want command fake, retryable only for exit 4 and a duration", tc.name, m)
		}
	}
}

func TestSynthesizedErrorUsesTheDefaultCodeForItsExitPlusTheStderrTail(t *testing.T) {
	want := map[int]string{3: "not_found", 4: "network_error", 5: "usage_error", 6: "forbidden", 8: "timeout", 9: "internal_error", 130: "interrupted"}
	for exit, code := range want {
		res := callFake(t, child{err: "first\nlast words\n", exit: exit}, 5)
		got := structured(res)
		message, _ := got["message"].(string)
		if !res.IsError || got["error"] != code || !strings.Contains(message, "last words") || meta(res)["synthesized"] != true || meta(res)["exit_code"] != float64(exit) {
			t.Errorf("exit %d: result = %v meta = %v, want a synthesized %s with the stderr tail", exit, got, meta(res), code)
		}
	}
}

func TestARealErrorObjectPassesThroughUnchangedAndIsNeverSynthesizedOver(t *testing.T) {
	line := `{"v":1,"error":"suite_not_found","message":"no such suite","scope":"read"}`
	res := callFake(t, child{out: line + "\n", err: "Error: no such suite\n", exit: 3}, 5)
	_, marked := meta(res)["synthesized"]
	if !res.IsError || structured(res)["error"] != "suite_not_found" || marked || text(res) != line {
		t.Errorf("isError = %v, structured = %v, synthesized key = %v, content = %q, want the object untouched", res.IsError, structured(res), marked, text(res))
	}
	res = callFake(t, child{out: progress + interrupted, exit: 130}, 5)
	events := eventList(res)
	_, marked = meta(res)["synthesized"]
	if !res.IsError || marked || len(events) != 2 || events[1]["interrupted"] != true {
		t.Errorf("interrupted summary: isError = %v, synthesized key = %v, events = %v, want it passed through", res.IsError, marked, events)
	}
}

func TestStderrIsNeverReadAsStdout(t *testing.T) {
	res := callFake(t, child{err: errorLine("suite_not_found"), exit: 3}, 5)
	if got := structured(res)["error"]; got != "not_found" || meta(res)["synthesized"] != true {
		t.Errorf("an error object on stderr gave %v, want a synthesized not_found", got)
	}
	res = callFake(t, child{out: oneShot, err: "STDERR-NOISE\n"}, 5)
	if res.IsError || strings.Contains(text(res), "STDERR-NOISE") || text(res) != strings.TrimSpace(oneShot) {
		t.Errorf("success with stderr output: isError = %v, content = %q, want the stdout alone", res.IsError, text(res))
	}
}

func TestStderrIsCappedToItsTailSeparatelyFromStdout(t *testing.T) {
	res := callFake(t, child{err: "HEAD" + strings.Repeat("e", 100_000) + "TAIL", exit: 9}, 5)
	message, _ := structured(res)["message"].(string)
	if !strings.HasSuffix(message, "TAIL") || strings.Contains(message, "HEAD") || len(message) > stderrCap {
		t.Errorf("message has %d bytes, want at most %d, ending in TAIL and dropping HEAD", len(message), stderrCap)
	}
}

func TestNDJSONIsEventsEndingAtTheTerminalEventOrTheErrorObject(t *testing.T) {
	res := callFake(t, child{out: progress + progress + summary, exit: 1}, 5)
	events := eventList(res)
	if res.IsError || len(events) != 3 || events[2]["event"] != "summary" || structured(res)["truncated"] != false {
		t.Errorf("stream: isError = %v, structured = %v, want 3 events ending at summary, not truncated", res.IsError, structured(res))
	}
	res = callFake(t, child{out: `{"v":1,"event":"approved","approval":{"id":"a1"}}` + "\n"}, 5)
	if events := eventList(res); len(events) != 1 || events[0]["event"] != "approved" {
		t.Errorf("a stream whose only line is its terminal event: structured = %v, want the events shape", structured(res))
	}
	res = callFake(t, child{out: progress + errorLine("unauthorized"), exit: 4}, 5)
	events = eventList(res)
	_, marked := meta(res)["synthesized"]
	if !res.IsError || len(events) != 2 || events[1]["error"] != "unauthorized" || marked {
		t.Errorf("stream ending in an error object: events = %v, synthesized key = %v", events, marked)
	}
}

func TestNDJSONOverTheCapIsTruncatedAndStillEndsAtTheTerminalEvent(t *testing.T) {
	var flood strings.Builder
	for i := 0; i < 1500; i++ {
		flood.WriteString(`{"v":1,"event":"progress","pad":"` + strings.Repeat("x", 300) + `"}` + "\n")
	}
	res := callFake(t, child{out: flood.String() + summary, exit: 1}, 5)
	events := eventList(res)
	if res.IsError || structured(res)["truncated"] != true || len(events) == 0 || len(events) > 1500 || events[len(events)-1]["event"] != "summary" {
		t.Errorf("flood: isError = %v, truncated = %v, %d events, want truncated and ending at summary", res.IsError, structured(res)["truncated"], len(events))
	}
}

func TestChildKilledAtTimeoutSecondsIsATimeoutToolErrorWithExitEight(t *testing.T) {
	res := callFake(t, child{hang: true}, 1)
	m := meta(res)
	if duration, _ := m["duration_ms"].(float64); !res.IsError || structured(res)["error"] != "timeout" || m["synthesized"] != true || m["exit_code"] != float64(8) || duration < 1000 {
		t.Errorf("isError = %v, structured = %v, meta = %v, want a synthesized timeout with exit_code 8 after at least a second", res.IsError, structured(res), m)
	}
	res = callFake(t, child{out: progress, hang: true}, 1)
	events := eventList(res)
	if !res.IsError || len(events) != 2 || events[1]["error"] != "timeout" {
		t.Errorf("events before the kill: %v, want the progress event then the synthesized timeout", events)
	}
}

func TestAnExitThatIsNotAnOutcomeIsAnInternalErrorNeverASuccess(t *testing.T) {
	cases := map[string]child{
		"exit 2 with empty stdout, a Go panic":        {exit: 2},
		"exit 1 with truncated non-terminal stdout":   {out: progress + `{"v":1,"event":"test_fa`, exit: 1},
		"exit 7 with no approval on its last line":    {out: progress, exit: 7},
		"an exit outside the table":                   {out: oneShot, exit: 127},
		"exit 2 whose last line is not a summary":     {out: summary + progress, exit: 2},
		"exit 1 with a one-shot object and no events": {out: oneShot, exit: 1},
	}
	for name, c := range cases {
		res := callFake(t, c, 5)
		if !res.IsError || structured(res)["error"] != "internal_error" || meta(res)["synthesized"] != true {
			t.Errorf("%s: isError = %v, structured = %v, meta = %v, want a synthesized internal_error", name, res.IsError, structured(res), meta(res))
		}
	}
	res := callFake(t, child{out: progress + summary, exit: 2}, 5)
	if res.IsError || structured(res)["error"] != nil || meta(res)["exit_code"] != float64(2) {
		t.Errorf("exit 2 ending in a summary: isError = %v, structured = %v, want an outcome with exit_code 2", res.IsError, structured(res))
	}
}

func TestApprovalOutcomesAndRunnerErrorsKeepTheirOwnMeaning(t *testing.T) {
	expired := `{"v":1,"error":"approval_expired","message":"m","approval":{"id":"a1"}}` + "\n"
	res := callFake(t, child{out: progress + expired, exit: 7}, 5)
	if events := eventList(res); res.IsError || len(events) != 2 || events[1]["error"] != "approval_expired" {
		t.Errorf("exit 7 ending in an approval error object: isError = %v, events = %v, want a successful result", res.IsError, events)
	}
	res = callFake(t, child{out: errorLine("runner_error"), exit: 1}, 5)
	_, marked := meta(res)["synthesized"]
	if !res.IsError || structured(res)["error"] != "runner_error" || marked {
		t.Errorf("exit 1 with runner_error: isError = %v, structured = %v, synthesized key = %v, want the object passed through as an error", res.IsError, structured(res), marked)
	}
}
