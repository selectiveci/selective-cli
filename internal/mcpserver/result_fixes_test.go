package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	localFail = `{"v":1,"event":"test_fail","node":"n1","error":"boom"}` + "\n"
	tailFail  = `{"v":1,"event":"test_fail","id":"f1","node":"n1","file":"a_test.rb","line":3,"error":"Expected: 1\n  Actual: 2"}` + "\n"
)

func eventBeforeLast(res *mcp.CallToolResult) map[string]any {
	events := eventList(res)
	if len(events) < 2 {
		return nil
	}
	return events[len(events)-2]
}

func TestATestFailLineIsNeverTakenForAnErrorObject(t *testing.T) {
	for name, fail := range map[string]string{"localrun test_fail": localFail, "tail test_fail": tailFail} {
		for _, exit := range []int{1, 2} {
			res := callCanned(t, Result{Stdout: []byte(progress + fail), ExitCode: exit})
			events := eventList(res)
			if !res.IsError || len(events) != 3 || events[1]["event"] != "test_fail" || events[2]["error"] != "internal_error" || meta(res)["synthesized"] != true {
				t.Errorf("%s at exit %d: isError = %v, events = %v, want the test_fail kept and a synthesized internal_error last", name, exit, res.IsError, events)
			}
		}
		res := callCanned(t, Result{Stdout: []byte(progress + fail), ExitCode: -1, TimedOut: true})
		if !res.IsError || errorOf(res)["error"] != "timeout" || meta(res)["synthesized"] != true {
			t.Errorf("%s then a timeout: isError = %v, events = %v, want a synthesized timeout last", name, res.IsError, eventList(res))
		}
	}
}

func TestAnOneShotOverTheCapIsAnInternalErrorNamingTheCapAndATruncatedOneShotKeepsTheEventsShape(t *testing.T) {
	big := `{"v":1,"flakes":[` + strings.Repeat(`{"id":"x"},`, 30000) + `{"id":"y"}],"next_cursor":null}` + "\n"
	res := callFake(t, child{out: big}, 5)
	message, _ := errorOf(res)["message"].(string)
	if !res.IsError || errorOf(res)["error"] != "internal_error" || !strings.Contains(message, fmt.Sprint(stdoutCap)) || meta(res)["synthesized"] != true || structured(res)["truncated"] != true {
		t.Errorf("over the cap: isError = %v, structured = %v, meta = %v, want a synthesized internal_error naming %d and truncated", res.IsError, structured(res), meta(res), stdoutCap)
	}
	res = callFake(t, child{out: oneShot, hold: true}, 5)
	if res.IsError || len(eventList(res)) != 1 || structured(res)["truncated"] != true {
		t.Errorf("held one-shot: isError = %v, structured = %v, want one event in the events shape with truncated true", res.IsError, structured(res))
	}
}

func TestContentTextIsTheChildsLineByteForByteIncludingHTMLCharacters(t *testing.T) {
	line := `{"v":1,"error":"suite_not_found","message":"expected <nil> & got #<Suite id=1>"}`
	res := callFake(t, child{out: line + "\n", exit: 3}, 5)
	if text(res) != line {
		t.Errorf("content = %s, want %s", text(res), line)
	}
	res = callFake(t, child{out: progress + line + "\n", exit: 3}, 5)
	if !strings.Contains(text(res), `"message":"expected <nil> & got #<Suite id=1>"`) {
		t.Errorf("events content = %s, want the characters < > & unescaped", text(res))
	}
}

func TestStdoutAndStderrDrainsShareOneDeadlineWhenADescendantHoldsBothPipes(t *testing.T) {
	res := callFake(t, child{out: oneShot, hold: true}, 10)
	limit := pipeDrainWindow + 500*time.Millisecond
	if got := time.Duration(meta(res)["duration_ms"].(float64)) * time.Millisecond; got > limit {
		t.Errorf("returned after %v, want within %v: one drain window plus 0.5s", got, limit)
	}
}

func TestAStartFailureIsAnInternalErrorToolResultAndInvalidParamsStayProtocolErrors(t *testing.T) {
	handler := NewRunner(fakeDocument, "/nonexistent/sel", Exposure{}).Handler()
	call := func(arguments string) (*mcp.CallToolResult, error) {
		return handler(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "fake", Arguments: json.RawMessage(arguments)}})
	}
	res, err := call(`{}`)
	if err != nil || res == nil || !res.IsError || !strings.Contains(text(res), `"internal_error"`) || meta(res)["synthesized"] != true || meta(res)["command"] != "fake" {
		t.Errorf("start failure: res = %+v, err = %v, want a synthesized internal_error tool result with _meta", res, err)
	}
	_, err = call(`{"bogus":1}`)
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) || wire.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("unknown property: err = %v, want invalid params", err)
	}
}

func TestTheLastNonEmptyStdoutLineMustItselfBeTheTerminal(t *testing.T) {
	res := callCanned(t, Result{Stdout: []byte(progress + summary + "not json\n"), ExitCode: 1})
	if !res.IsError || errorOf(res)["error"] != "internal_error" || meta(res)["synthesized"] != true {
		t.Errorf("garbage after the summary: isError = %v, events = %v, want a synthesized internal_error", res.IsError, eventList(res))
	}
	res = callCanned(t, Result{Stdout: []byte(progress + summary + "\n\n"), ExitCode: 1})
	if res.IsError {
		t.Errorf("blank lines after the summary: isError = true, events = %v, want an outcome", eventList(res))
	}
}

func TestExitSevenNeedsItsOwnTerminalNotAnyApprovalKey(t *testing.T) {
	withApproval := `{"v":1,"event":"progress","approval":{"id":"a1"}}` + "\n"
	res := callCanned(t, Result{Stdout: []byte(withApproval), ExitCode: 7})
	if !res.IsError || errorOf(res)["error"] != "internal_error" || meta(res)["synthesized"] != true {
		t.Errorf("progress carrying approval: isError = %v, events = %v, want a synthesized internal_error", res.IsError, eventList(res))
	}
}

func TestATimeoutAlwaysEndsWithASynthesizedTimeoutAndKeepsWhatTheChildPrinted(t *testing.T) {
	cases := []struct {
		name       string
		res        *mcp.CallToolResult
		key, value string
	}{
		{"a real error then a hang", callFake(t, child{out: errorLine("suite_not_found"), hang: true}, 1), "error", "suite_not_found"},
		{"a summary then a hang", callFake(t, child{out: progress + summary, hang: true}, 1), "event", "summary"},
		{"an interrupted summary, then exit 130 after the kill", callFake(t, child{out: progress + interrupted, trap: true}, 1), "event", "summary"},
	}
	for _, c := range cases {
		events := eventList(c.res)
		if !c.res.IsError || errorOf(c.res)["error"] != "timeout" || meta(c.res)["synthesized"] != true || meta(c.res)["exit_code"] != float64(8) || eventBeforeLast(c.res)[c.key] != c.value {
			t.Errorf("%s: isError = %v, events = %v, meta = %v, want the child's line kept and a synthesized timeout last with exit_code 8", c.name, c.res.IsError, events, meta(c.res))
		}
	}
}
