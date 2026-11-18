package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	initializeFrame  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`
	initializedFrame = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	listFrame        = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	versionFrame     = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"version","arguments":{}}}`
	orgsFrame        = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"orgs","arguments":{}}}`
)

type frameStream struct {
	in  io.WriteCloser
	out *bufio.Reader
}

func (s frameStream) send(t *testing.T, frame string) {
	t.Helper()
	if _, err := io.WriteString(s.in, frame+"\n"); err != nil {
		t.Fatalf("send %s: %v", frame, err)
	}
}

func (s frameStream) next(t *testing.T) (string, map[string]any) {
	t.Helper()
	line, err := s.out.ReadString('\n')
	if err != nil {
		t.Fatalf("read a frame: %v (got %q)", err, line)
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(line), &frame); err != nil || frame["jsonrpc"] != "2.0" {
		t.Fatalf("stdout line %q is not a JSON-RPC frame (err %v)", line, err)
	}
	return line, frame
}

func (s frameStream) response(t *testing.T, id float64) map[string]any {
	t.Helper()
	for {
		if _, frame := s.next(t); frame["id"] == id {
			return frame
		}
	}
}

func handshake(t *testing.T, s frameStream) {
	t.Helper()
	s.send(t, initializeFrame)
	s.response(t, 1)
	s.send(t, initializedFrame)
}

func TestMCPCommandIsHiddenFromMCPAndDeclaresItsModesAndExits(t *testing.T) {
	cmd, _, err := newRoot().Find([]string{"mcp"})
	if err != nil || cmd.Name() != "mcp" {
		t.Fatalf("Find(mcp) = %v, %v, want the mcp command", cmd.Name(), err)
	}
	if cmd.Annotations["mcp"] != "skip" || cmd.Annotations["exit_codes"] != "0,5,9" {
		t.Errorf("annotations = %v, want mcp skip and exit_codes 0,5,9", cmd.Annotations)
	}
	for _, name := range []string{"read-only", "allow-destructive"} {
		if f := cmd.Flags().Lookup(name); f == nil || f.Value.Type() != "bool" {
			t.Errorf("flag --%s = %v, want a bool flag", name, f)
		}
	}
}

func TestMCPCommandRefusesBothModeFlagsBeforeServing(t *testing.T) {
	root := newRoot()
	root.SetIn(strings.NewReader(initializeFrame + "\n"))
	var stdout, stderr bytes.Buffer
	code := run(root, []string{"mcp", "--read-only", "--allow-destructive"}, &stdout, &stderr)
	if code != 5 {
		t.Errorf("exit = %d, want 5", code)
	}
	if !strings.Contains(stderr.String(), "read-only") || !strings.Contains(stderr.String(), "allow-destructive") {
		t.Errorf("stderr = %q, want it to name both flags", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"error":"usage_error"`) || strings.Contains(stdout.String(), "jsonrpc") {
		t.Errorf("stdout = %q, want the usage_error object and no JSON-RPC frame", stdout.String())
	}
}

func TestMCPCommandKeepsStdoutToFramesWhenStdinClosesBeforeTheHandshakeEnds(t *testing.T) {
	root := newRoot()
	root.SetIn(strings.NewReader(initializeFrame + "\n"))
	var stdout, stderr bytes.Buffer
	run(root, []string{"mcp"}, &stdout, &stderr)
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); line != "" && (err != nil || frame["jsonrpc"] != "2.0") {
			t.Errorf("stdout line %q is not a JSON-RPC frame", line)
		}
	}
}

func TestMCPCommandServesTheToolsOfItsModeAndExitsZeroWhenStdinCloses(t *testing.T) {
	cases := []struct {
		mode  string
		args  []string
		tools int
	}{
		{"default", nil, 30},
		{"allow-destructive", []string{"--allow-destructive"}, 33},
		{"read-only", []string{"--read-only"}, 19},
	}
	for _, c := range cases {
		root := newRoot()
		stdinReader, stdinWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stdoutReader, stdoutWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		root.SetIn(stdinReader)
		exit := make(chan int, 1)
		go func() {
			exit <- run(root, append([]string{"mcp"}, c.args...), stdoutWriter, io.Discard)
			_ = stdoutWriter.Close()
		}()
		stream := frameStream{in: stdinWriter, out: bufio.NewReader(stdoutReader)}
		handshake(t, stream)
		stream.send(t, listFrame)
		result, _ := stream.response(t, 2)["result"].(map[string]any)
		tools, _ := result["tools"].([]any)
		if len(tools) != c.tools {
			t.Errorf("%s: %d tools, want %d", c.mode, len(tools), c.tools)
		}
		_ = stdinWriter.Close()
		select {
		case code := <-exit:
			if code != 0 {
				t.Errorf("%s: exit = %d after stdin closed, want 0", c.mode, code)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: sel mcp did not exit after stdin closed", c.mode)
		}
	}
}

func TestMCPCommandStdoutCarriesOnlyJSONRPCFramesWhenAnUpdateCheckIsDue(t *testing.T) {
	due := dueUpdateCheck(t)
	if out, err := exec.Command(due.binary, "version", "--json").CombinedOutput(); err != nil || due.requests.Load() == 0 {
		t.Fatalf("control: a due check made %d release requests (err %v): %s", due.requests.Load(), err, out)
	}
	due.seed()
	serveToolCallsAndCheckStdout(t, due.binary)
}

func TestMCPCommandStdoutCarriesOnlyJSONRPCFramesWhenANewerReleaseIsKnown(t *testing.T) {
	due := dueUpdateCheck(t)
	cached := `{"latest_version":"2.0.0","checked_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(os.Getenv("SELECTIVE_CONFIG_DIR"), "update-check.json"), []byte(cached), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(due.binary, "version", "--json").Output(); err != nil || !strings.Contains(string(out), `"update_available":true`) {
		t.Fatalf("control: version says %s (err %v), want an update available", out, err)
	}
	serveToolCallsAndCheckStdout(t, due.binary)
}

func serveToolCallsAndCheckStdout(t *testing.T, binary string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stream := frameStream{in: stdin, out: bufio.NewReader(stdout)}
	handshake(t, stream)
	for i, frame := range []string{listFrame, versionFrame, orgsFrame} {
		id := float64(i + 2)
		stream.send(t, frame)
		if resp := stream.response(t, id); resp["error"] != nil {
			t.Errorf("request %v got a protocol error: %v", id, resp["error"])
		}
	}
	_ = stdin.Close()
	rest, _ := io.ReadAll(stream.out)
	for _, line := range strings.Split(strings.TrimSpace(string(rest)), "\n") {
		if line == "" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil || frame["jsonrpc"] != "2.0" {
			t.Errorf("stdout line after the last response %q is not a JSON-RPC frame", line)
		}
	}
	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Errorf("exit = %d, want 0; stderr: %s", exit.ExitCode(), stderr.String())
		} else {
			t.Error(err)
		}
	}
}
