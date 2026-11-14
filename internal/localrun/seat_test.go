package localrun

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type seatHarness struct {
	ln     net.Listener
	stdin  *io.PipeWriter
	stdout *bytes.Buffer
	code   chan int
}

func startSeat(t *testing.T) *seatHarness {
	t.Helper()
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	t.Setenv(socketEnv, sock)
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { stdinW.Close() })
	h := &seatHarness{ln: ln, stdin: stdinW, stdout: &bytes.Buffer{}, code: make(chan int, 1)}
	go func() { h.code <- RunSeat([]string{"--stdio"}, stdinR, h.stdout, io.Discard) }()
	return h
}

func (h *seatHarness) accept(t *testing.T) net.Conn {
	t.Helper()
	_ = h.ln.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	conn, err := h.ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func (h *seatHarness) awaitCode(t *testing.T) int {
	t.Helper()
	select {
	case c := <-h.code:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("the seat did not exit within 3s")
		return -1
	}
}

func TestSeatHelpMentionsStdioAndWritesNothingToStdout(t *testing.T) {
	var out, errw bytes.Buffer
	if code := RunSeat([]string{"--help"}, strings.NewReader(""), &out, &errw); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if !strings.Contains(errw.String(), "--stdio") || out.Len() != 0 {
		t.Errorf("stderr %q stdout %q, want --stdio on stderr and nothing on stdout", errw.String(), out.String())
	}
}

func TestSeatRefusesUnknownArgumentsWithExit2AndNothingOnStdout(t *testing.T) {
	for _, args := range [][]string{nil, {"--bogus"}, {"ws://localhost/transport/websocket?api_key=x", "runner-1"}} {
		var out, errw bytes.Buffer
		if code := RunSeat(args, strings.NewReader(""), &out, &errw); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if out.Len() != 0 || errw.Len() == 0 {
			t.Errorf("%q: stdout %q stderr %q, want only a stderr message", args, out.String(), errw.String())
		}
	}
}

func TestSeatEndsTheStreamOnTheExitLineWithoutForwardingIt(t *testing.T) {
	h := startSeat(t)
	conn := h.accept(t)
	_, _ = io.WriteString(h.stdin, "{\"type\":\"ping\"}\nexit\n")
	if c := h.awaitCode(t); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, _ := io.ReadAll(conn)
	if string(got) != "{\"type\":\"ping\"}\n" {
		t.Errorf("orchestrator received %q, want only the ping frame", got)
	}
}

func TestSeatEndsWhenTheClientClosesItsStdin(t *testing.T) {
	h := startSeat(t)
	h.accept(t)
	h.stdin.Close()
	if c := h.awaitCode(t); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
}

func TestSeatRelaysOrchestratorFramesToStdoutAndEndsWhenItHangsUp(t *testing.T) {
	h := startSeat(t)
	conn := h.accept(t)
	_, _ = io.WriteString(conn, "{\"command\":\"connected\"}\n")
	conn.Close()
	if c := h.awaitCode(t); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
	if got := h.stdout.String(); got != "{\"command\":\"connected\"}\n" {
		t.Errorf("client stdout = %q, want the connected frame", got)
	}
}

func TestSeatStdioWithoutASocketIsARefusal(t *testing.T) {
	t.Setenv(socketEnv, "")
	var out, errw bytes.Buffer
	if code := RunSeat([]string{"--stdio"}, strings.NewReader(""), &out, &errw); code != 2 || out.Len() != 0 || errw.Len() == 0 {
		t.Errorf("exit %d stdout %q stderr %q, want exit 2 and only a stderr message", code, out.String(), errw.String())
	}
}
