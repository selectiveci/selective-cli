package localrun

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seatRunner(t *testing.T) (*Seat, *io.PipeWriter, io.Reader, chan int) {
	t.Helper()
	seat, err := NewSeat(os.Args[0])
	if err != nil {
		t.Fatalf("NewSeat: %v", err)
	}
	t.Cleanup(func() { seat.Close() })
	for _, kv := range seat.Env() {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	seatStdin, clientStdout := io.Pipe()
	clientStdin, seatStdout := io.Pipe()
	code := make(chan int, 1)
	go func() { code <- RunSeat([]string{"--stdio"}, seatStdin, seatStdout, io.Discard) }()
	t.Cleanup(func() { clientStdout.Close() })
	return seat, clientStdout, clientStdin, code
}

func accept(t *testing.T, seat *Seat) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := seat.Accept(ctx)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func awaitCode(t *testing.T, code chan int) int {
	t.Helper()
	select {
	case c := <-code:
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

func TestSeatDeliversTheOrchestratorsConnectedFrameFirst(t *testing.T) {
	seat, _, seatOut, _ := seatRunner(t)
	conn := accept(t, seat)
	go func() {
		_, _ = Run(context.Background(), conn, conn, io.Discard, Options{Selector: selecting(), Timeout: 3 * time.Second})
	}()

	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(seatOut).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, `"command":"connected"`) {
			t.Errorf("first frame = %q, want connected", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no frame reached the client within 3s")
	}
}

func TestSeatCarriesAFakeClientThroughATwoTestRun(t *testing.T) {
	seat, clientStdout, clientStdin, code := seatRunner(t)
	conn := accept(t, seat)
	events := &stampedLines{}
	client := &fakeClient{t: t, commands: make(chan map[string]any, 64), frames: clientStdout, events: events}
	go client.readCommands(clientStdin)
	scripted := make(chan struct{})
	go func() {
		defer close(scripted)
		client.handshake("a", "b", "c")
		ids, _ := client.next("run_test_cases")["test_case_ids"].([]any)
		if len(ids) != 2 {
			t.Errorf("run_test_cases ids = %v, want 2", ids)
		}
		client.result("a", "passed")
		client.result("c", "failed")
		client.finish()
		_, _ = io.WriteString(clientStdout, "exit\n")
	}()
	sum, err := Run(context.Background(), conn, conn, events, Options{Selector: selecting("a", "c"), Timeout: 3 * time.Second})
	<-scripted
	if err != nil || sum.Passed != 1 || sum.Failed != 1 || sum.Selected != 2 || sum.Skipped != 1 {
		t.Errorf("summary %+v err %v, want 1 passed 1 failed 2 selected 1 skipped", sum, err)
	}
	if c := awaitCode(t, code); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
}

func TestSeatEndsTheStreamOnTheExitLineWithoutForwardingIt(t *testing.T) {
	seat, clientStdout, _, code := seatRunner(t)
	conn := accept(t, seat)
	_, _ = io.WriteString(clientStdout, "{\"type\":\"ping\"}\nexit\n")
	if c := awaitCode(t, code); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, _ := io.ReadAll(conn)
	if string(got) != "{\"type\":\"ping\"}\n" {
		t.Errorf("orchestrator received %q, want only the ping frame", got)
	}
}

func TestSeatEndsWhenTheClientClosesItsStdin(t *testing.T) {
	seat, clientStdout, _, code := seatRunner(t)
	accept(t, seat)
	clientStdout.Close()
	if c := awaitCode(t, code); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
}

func TestSeatEndsWhenTheOrchestratorHangsUp(t *testing.T) {
	seat, _, _, code := seatRunner(t)
	accept(t, seat).Close()
	if c := awaitCode(t, code); c != 0 {
		t.Errorf("seat exit %d, want 0", c)
	}
}

func TestSeatStdioWithoutASocketIsARefusal(t *testing.T) {
	t.Setenv("SELECTIVE_SEAT_SOCKET", "")
	var out, errw bytes.Buffer
	if code := RunSeat([]string{"--stdio"}, strings.NewReader(""), &out, &errw); code != 2 || out.Len() != 0 || errw.Len() == 0 {
		t.Errorf("exit %d stdout %q stderr %q, want exit 2 and only a stderr message", code, out.String(), errw.String())
	}
}

func TestNewSeatWritesAShimThatExecsTheGivenBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dir with 'quote")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, "sel")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	seat, err := NewSeat(fake)
	if err != nil {
		t.Fatal(err)
	}
	defer seat.Close()
	var shim, sock string
	for _, kv := range seat.Env() {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "SELECTIVE_TRANSPORT":
			shim = value
		case "SELECTIVE_SEAT_SOCKET":
			sock = value
		}
	}
	if info, err := os.Stat(filepath.Dir(shim)); err != nil || info.Mode().Perm() != 0o700 || filepath.Dir(sock) != filepath.Dir(shim) {
		t.Errorf("temp dir %v %v, want 0700 holding the shim and the socket", info, err)
	}
	out, err := exec.Command(shim, "--help", "x y").Output()
	if err != nil || strings.TrimSpace(string(out)) != "__transport --help x y" {
		t.Errorf("shim output %q err %v, want the arguments passed to __transport", out, err)
	}
}

func TestNewSeatKeepsTheSocketPathShortWhenTmpdirIsLong(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 90))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	seat, err := NewSeat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer seat.Close()
	for _, kv := range seat.Env() {
		if sock, ok := strings.CutPrefix(kv, "SELECTIVE_SEAT_SOCKET="); ok && len(sock) > 100 {
			t.Errorf("socket path is %d bytes, want 100 or fewer", len(sock))
		}
	}
}

func TestSeatCloseRemovesItsDirectory(t *testing.T) {
	seat, err := NewSeat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(strings.TrimPrefix(seat.Env()[0], "SELECTIVE_TRANSPORT="))
	seat.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want it removed", dir, err)
	}
}
