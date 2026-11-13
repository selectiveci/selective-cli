package localrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
)

type fakeClient struct {
	t        *testing.T
	commands chan map[string]any
	frames   *io.PipeWriter
}

func (c *fakeClient) next(want string) map[string]any {
	c.t.Helper()
	select {
	case cmd := <-c.commands:
		if cmd["command"] != want {
			c.t.Errorf("expected command %q, got %v", want, cmd)
		}
		return cmd
	case <-time.After(3 * time.Second):
		c.t.Errorf("no %q command within 3s", want)
		return map[string]any{}
	}
}

func (c *fakeClient) send(frameType string, data map[string]any) {
	line, _ := json.Marshal(map[string]any{"type": frameType, "data": data})
	_, _ = c.frames.Write(append(line, '\n'))
}

func (c *fakeClient) result(id, status string) {
	data := map[string]any{"id": id, "status": status, "file_path": "./spec/" + id + "_spec.rb", "line_number": 7, "run_time": 0.0123}
	if status == "failed" {
		data["failure_message_lines"] = []string{"expected 1", "got 2"}
	}
	c.send("test_case_result", data)
}

func (c *fakeClient) handshake(cases ...string) {
	c.next("connected")
	configure := c.next("configure_test_map")
	if configure["tree"] != true || configure["record"] != false {
		c.t.Errorf("configure_test_map must ask for the tree and not record: %v", configure)
	}
	c.next("test_manifest")
	var testCases []map[string]any
	for _, id := range cases {
		testCases = append(testCases, map[string]any{"id": id, "file_path": "./spec/" + id + "_spec.rb", "line_number": 7})
	}
	c.send("test_manifest", map[string]any{
		"test_cases":    testCases,
		"test_map_tree": map[string]any{"sha": "abc123", "prefix": "", "files": map[string]string{"app/a.rb": "b1"}, "dirty": []string{"app/a.rb"}},
	})
}

func (c *fakeClient) finish() {
	c.next("close")
	c.send("report_at_finish", map[string]any{"connection_retries": 0})
}

type outcome struct {
	events   []map[string]any
	summary  Summary
	err      error
	commands []map[string]any
}

func (o outcome) names() []string {
	var names []string
	for _, e := range o.events {
		names = append(names, e["event"].(string))
	}
	return names
}

func (o outcome) named(name string) []map[string]any {
	var out []map[string]any
	for _, e := range o.events {
		if e["event"] == name {
			out = append(out, e)
		}
	}
	return out
}

func selecting(ids ...string) Selector {
	return func(context.Context, Manifest) (Selection, error) {
		return Selection{Outcome: "select", Selected: ids, ChangedFiles: 1, Source: "map"}, nil
	}
}

func exercise(t *testing.T, opts Options, script func(*fakeClient)) outcome {
	t.Helper()
	return exerciseWriting(t, opts, nil, script)
}

type failingWriter struct {
	w     io.Writer
	after int
}

func (f *failingWriter) Write(b []byte) (int, error) {
	if f.after <= 0 {
		return 0, io.ErrClosedPipe
	}
	f.after--
	return f.w.Write(b)
}

func exerciseWriting(t *testing.T, opts Options, wrap func(io.Writer) io.Writer, script func(*fakeClient)) outcome {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 3 * time.Second
	}
	toClientR, toClientW := io.Pipe()
	fromClientR, fromClientW := io.Pipe()
	client := &fakeClient{t: t, commands: make(chan map[string]any, 64), frames: fromClientW}
	go func() {
		scanner := bufio.NewScanner(toClientR)
		for scanner.Scan() {
			var cmd map[string]any
			_ = json.Unmarshal(scanner.Bytes(), &cmd)
			client.commands <- cmd
		}
	}()
	scripted := make(chan struct{})
	go func() {
		defer close(scripted)
		script(client)
		fromClientW.Close()
	}()
	var events bytes.Buffer
	var sink io.Writer = &events
	if wrap != nil {
		sink = wrap(&events)
	}
	sum, err := Run(context.Background(), fromClientR, toClientW, sink, opts)
	toClientW.Close()
	fromClientR.Close()
	<-scripted
	out := outcome{summary: sum, err: err}
	for _, line := range strings.Split(strings.TrimSpace(events.String()), "\n") {
		if line == "" {
			continue
		}
		var e map[string]any
		if jerr := json.Unmarshal([]byte(line), &e); jerr != nil {
			t.Fatalf("event line is not JSON: %q", line)
		}
		if e["v"] != float64(1) {
			t.Errorf("event line lacks v:1: %q", line)
		}
		out.events = append(out.events, e)
	}
	for len(client.commands) > 0 {
		out.commands = append(out.commands, <-client.commands)
	}
	return out
}

func codeOf(err error) contract.Code {
	if coded, ok := err.(contract.Coded); ok {
		return coded.ErrorCode()
	}
	return ""
}

func TestRunStreamsSelectedTestsAndSummarises(t *testing.T) {
	var seen Manifest
	selector := func(_ context.Context, m Manifest) (Selection, error) {
		seen = m
		return selecting("a", "b")(context.Background(), m)
	}
	var batch []any
	out := exercise(t, Options{Selector: selector}, func(c *fakeClient) {
		c.handshake("a", "b", "c")
		batch, _ = c.next("run_test_cases")["test_case_ids"].([]any)
		c.result("a", "passed")
		c.result("b", "pending")
		c.finish()
	})
	if out.err != nil {
		t.Fatalf("Run: %v", out.err)
	}
	if !reflect.DeepEqual(batch, []any{"a", "b"}) {
		t.Errorf("run_test_cases ids = %v", batch)
	}
	if len(seen.Cases) != 3 || seen.Cases[2] != (Case{ID: "c", FilePath: "./spec/c_spec.rb"}) || !strings.Contains(string(seen.Tree), `"sha":"abc123"`) {
		t.Errorf("selector saw %+v", seen)
	}
	want := []string{"selection", "summary"}
	if !reflect.DeepEqual(out.names(), want) {
		t.Fatalf("events = %v, want %v", out.names(), want)
	}
	selection := out.events[0]
	if selection["outcome"] != "select" || selection["selected"] != float64(2) || selection["skipped"] != float64(1) || selection["total"] != float64(3) || selection["changed_files"] != float64(1) || selection["source"] != "map" || selection["cause"] != nil {
		t.Errorf("selection = %v", selection)
	}
	last := out.events[1]
	if last["passed"] != float64(1) || last["pending"] != float64(1) || last["failed"] != float64(0) || last["selected"] != float64(2) {
		t.Errorf("summary = %v", last)
	}
}

func TestRunCountsAFailedResultInTheSummary(t *testing.T) {
	out := exercise(t, Options{Selector: selecting("a", "b")}, func(c *fakeClient) {
		c.handshake("a", "b")
		c.next("run_test_cases")
		c.result("a", "failed")
		c.result("b", "passed")
		c.finish()
	})
	last := out.events[len(out.events)-1]
	if out.err != nil || out.summary.Failed != 1 || last["event"] != "summary" || last["failed"] != float64(1) {
		t.Errorf("err=%v summary=%+v last=%v", out.err, out.summary, last)
	}
}

func TestRunRejectsAFinishReportWhileResultsAreOutstanding(t *testing.T) {
	out := exercise(t, Options{Selector: selecting("a", "b")}, func(c *fakeClient) {
		c.handshake("a", "b")
		c.next("run_test_cases")
		c.result("a", "passed")
		c.send("report_at_finish", map[string]any{"connection_retries": 0})
	})
	if codeOf(out.err) != contract.RunnerError {
		t.Fatalf("err = %v, want runner_error", out.err)
	}
	if got := out.names(); !reflect.DeepEqual(got, []string{"selection"}) {
		t.Errorf("events = %v: an incomplete run must not summarise", got)
	}
}

func TestRunRejectsAManifestWithoutATestCaseList(t *testing.T) {
	frames := map[string]string{
		"null data": `{"type":"test_manifest","data":null}`,
		"null list": `{"type":"test_manifest","data":{"test_cases":null}}`,
		"no list":   `{"type":"test_manifest","data":{}}`,
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			var closed map[string]any
			out := exercise(t, Options{Selector: selecting()}, func(c *fakeClient) {
				c.next("connected")
				c.next("configure_test_map")
				c.next("test_manifest")
				_, _ = c.frames.Write([]byte(frame + "\n"))
				closed = c.next("close")
			})
			if codeOf(out.err) != contract.RunnerError || closed["command"] != "close" || len(out.events) != 0 {
				t.Fatalf("err=%v close=%v events=%v", out.err, closed, out.events)
			}
		})
	}
}

func TestRunAcceptsAnEmptyManifestList(t *testing.T) {
	out := exercise(t, Options{Selector: selecting()}, func(c *fakeClient) {
		c.next("connected")
		c.next("configure_test_map")
		c.next("test_manifest")
		c.send("test_manifest", map[string]any{"test_cases": []any{}})
		c.finish()
	})
	if out.err != nil || out.summary.Selected != 0 {
		t.Fatalf("err=%v summary=%+v", out.err, out.summary)
	}
}

func TestRunFailsWhenTheEventStreamCannotBeWritten(t *testing.T) {
	for _, writable := range []int{0, 1} {
		t.Run(fmt.Sprintf("writes before failure=%d", writable), func(t *testing.T) {
			wrap := func(w io.Writer) io.Writer { return &failingWriter{w: w, after: writable} }
			out := exerciseWriting(t, Options{Selector: selecting("a")}, wrap, func(c *fakeClient) {
				c.handshake("a")
				c.next("run_test_cases")
				c.result("a", "passed")
				c.next("close")
				c.send("report_at_finish", map[string]any{"connection_retries": 0})
			})
			if codeOf(out.err) != contract.InternalError || !strings.Contains(out.err.Error(), "event stream") {
				t.Fatalf("err = %v, want an internal_error about the event stream", out.err)
			}
		})
	}
}

func TestRunRejectsAResultWithAnUnknownStatus(t *testing.T) {
	for _, status := range []string{"", "skipped"} {
		t.Run("status="+status, func(t *testing.T) {
			out := exercise(t, Options{Selector: selecting("a")}, func(c *fakeClient) {
				c.handshake("a")
				c.next("run_test_cases")
				c.result("a", status)
			})
			if codeOf(out.err) != contract.RunnerError || out.summary.Passed != 0 {
				t.Fatalf("err = %v, summary = %+v", out.err, out.summary)
			}
		})
	}
}

func TestRunSendsEachSelectedIdOnce(t *testing.T) {
	var batch []any
	out := exercise(t, Options{Selector: selecting("a", "a", "b")}, func(c *fakeClient) {
		c.handshake("a", "b", "c")
		batch, _ = c.next("run_test_cases")["test_case_ids"].([]any)
		c.result("a", "passed")
		c.result("b", "passed")
		c.finish()
	})
	if !reflect.DeepEqual(batch, []any{"a", "b"}) {
		t.Errorf("run_test_cases ids = %v", batch)
	}
	if got := out.named("selection")[0]; got["selected"] != float64(2) || got["skipped"] != float64(1) || got["total"] != float64(3) {
		t.Errorf("selection = %v", got)
	}
}

func TestRunRunsEverythingOnAFullOutcome(t *testing.T) {
	selector := func(context.Context, Manifest) (Selection, error) {
		return Selection{Outcome: OutcomeFull, Cause: "no_map"}, nil
	}
	var batch []any
	out := exercise(t, Options{Selector: selector}, func(c *fakeClient) {
		c.handshake("a", "b")
		batch, _ = c.next("run_test_cases")["test_case_ids"].([]any)
		c.result("a", "passed")
		c.result("b", "passed")
		c.finish()
	})
	if !reflect.DeepEqual(batch, []any{"a", "b"}) {
		t.Errorf("a full outcome must run every manifest id, got %v", batch)
	}
	if got := out.named("selection")[0]; got["outcome"] != "full" || got["cause"] != "no_map" || got["skipped"] != float64(0) {
		t.Errorf("selection = %v", got)
	}
}

func TestRunClosesWithoutRunningWhenNothingIsSelected(t *testing.T) {
	out := exercise(t, Options{Selector: selecting()}, func(c *fakeClient) {
		c.handshake("a")
		c.finish()
	})
	if out.err != nil || out.summary.Selected != 0 || out.summary.Skipped != 1 {
		t.Fatalf("err=%v summary=%+v", out.err, out.summary)
	}
	if got := out.names(); !reflect.DeepEqual(got, []string{"selection", "summary"}) {
		t.Errorf("events = %v", got)
	}
}

func TestRunDoesNotCloseOnADuplicateResult(t *testing.T) {
	var early map[string]any
	out := exercise(t, Options{Selector: selecting("a", "b")}, func(c *fakeClient) {
		c.handshake("a", "b")
		c.next("run_test_cases")
		c.result("a", "passed")
		c.result("a", "passed")
		select {
		case early = <-c.commands:
		case <-time.After(150 * time.Millisecond):
		}
		c.result("b", "passed")
		c.finish()
	})
	if early != nil {
		t.Fatalf("closed early on a duplicate result: %v", early)
	}
	if out.err != nil || out.summary.Passed != 2 {
		t.Errorf("err=%v summary=%+v", out.err, out.summary)
	}
}

func TestRunReadsAManifestLargerThanAScannerLine(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 6000; i++ {
		files[fmt.Sprintf("app/models/file_%d.rb", i)] = strings.Repeat("a", 40)
	}
	var treeBytes int
	selector := func(_ context.Context, m Manifest) (Selection, error) {
		treeBytes = len(m.Tree)
		return Selection{Outcome: "select"}, nil
	}
	out := exercise(t, Options{Selector: selector}, func(c *fakeClient) {
		c.next("connected")
		c.next("configure_test_map")
		c.next("test_manifest")
		c.send("test_manifest", map[string]any{"test_cases": []any{}, "test_map_tree": map[string]any{"sha": "s", "files": files}})
		c.finish()
	})
	if out.err != nil || treeBytes < 200_000 {
		t.Fatalf("err=%v tree bytes=%d", out.err, treeBytes)
	}
}

func TestRunReportsARunnerThatDiesMidRun(t *testing.T) {
	out := exercise(t, Options{Selector: selecting("a", "b")}, func(c *fakeClient) {
		c.handshake("a", "b")
		c.next("run_test_cases")
		c.result("a", "failed")
	})
	if codeOf(out.err) != contract.RunnerError {
		t.Fatalf("err = %v, want code runner_error", out.err)
	}
	if got := out.names(); !reflect.DeepEqual(got, []string{"selection"}) {
		t.Errorf("events before the death = %v", got)
	}
	var terminal bytes.Buffer
	if err := format.Error(&terminal, out.err.(contract.Coded)); err != nil || !strings.HasPrefix(terminal.String(), `{"v":1,"error":"runner_error","message":"`) {
		t.Errorf("terminal object = %q (%v)", terminal.String(), err)
	}
}

func TestRunReportsAnUnreadableFrame(t *testing.T) {
	var closed map[string]any
	out := exercise(t, Options{Selector: selecting("a")}, func(c *fakeClient) {
		c.next("connected")
		c.next("configure_test_map")
		c.next("test_manifest")
		_, _ = c.frames.Write([]byte("not json\n"))
		closed = c.next("close")
	})
	if codeOf(out.err) != contract.RunnerError || closed["command"] != "close" {
		t.Fatalf("err = %v, close = %v", out.err, closed)
	}
}

func TestRunTimesOutWhenResultsStayShortOfTheIdsSent(t *testing.T) {
	var closed map[string]any
	started := time.Now()
	out := exercise(t, Options{Selector: selecting("a", "b"), Timeout: 150 * time.Millisecond}, func(c *fakeClient) {
		c.handshake("a", "b")
		c.next("run_test_cases")
		c.result("a", "passed")
		closed = c.next("close")
		time.Sleep(10 * time.Millisecond)
	})
	if codeOf(out.err) != contract.Timeout || contract.ExitFor(codeOf(out.err), 0) != contract.ExitTimeout {
		t.Fatalf("err = %v", out.err)
	}
	if !strings.Contains(out.err.Error(), "1 of 2") || closed["command"] != "close" {
		t.Errorf("message=%q close=%v", out.err, closed)
	}
	if time.Since(started) > 2*time.Second {
		t.Errorf("took %s", time.Since(started))
	}
}

func TestRunClosesTheRunnerWhenSelectionFails(t *testing.T) {
	selector := func(context.Context, Manifest) (Selection, error) {
		return Selection{}, contract.Errorf(contract.SuiteNotFound, "no such suite")
	}
	var closed bool
	out := exercise(t, Options{Selector: selector}, func(c *fakeClient) {
		c.handshake("a")
		c.next("close")
		closed = true
	})
	if codeOf(out.err) != contract.SuiteNotFound || !closed {
		t.Fatalf("err=%v closed=%v", out.err, closed)
	}
}

type stuckWriter struct {
	release <-chan struct{}
	on      string
}

func (s *stuckWriter) Write(b []byte) (int, error) {
	if strings.Contains(string(b), s.on) {
		<-s.release
		return 0, io.ErrClosedPipe
	}
	return len(b), nil
}

func TestRunTimesOutWhenACommandWriteBlocks(t *testing.T) {
	for _, command := range []string{"connected", "run_test_cases"} {
		t.Run(command, func(t *testing.T) { runWithBlockedWrite(t, command) })
	}
}

func runWithBlockedWrite(t *testing.T, command string) {
	t.Helper()
	release := make(chan struct{})
	defer close(release)
	framesIn, framesOut := io.Pipe()
	defer framesOut.Close()
	go func() {
		manifest := `{"type":"test_manifest","data":{"test_cases":[{"id":"a","file_path":"./a.rb"}]}}` + "\n"
		_, _ = framesOut.Write([]byte(manifest))
	}()
	type ended struct {
		err error
		at  time.Duration
	}
	finished := make(chan ended, 1)
	started := time.Now()
	go func() {
		_, err := Run(context.Background(), framesIn, &stuckWriter{release: release, on: command}, io.Discard, Options{Selector: selecting("a"), Timeout: 200 * time.Millisecond})
		finished <- ended{err, time.Since(started)}
	}()
	select {
	case got := <-finished:
		if codeOf(got.err) != contract.Timeout || got.at > time.Second {
			t.Fatalf("err=%v after %s, want a timeout within the deadline", got.err, got.at)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run is stuck in a blocked command write past its timeout")
	}
}
