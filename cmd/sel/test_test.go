package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/localrun"
)

func TestTransportExistsButIsAbsentFromHelpJSON(t *testing.T) {
	found := false
	for _, c := range newRoot().Commands() {
		found = found || c.Name() == "__transport"
	}
	if !found {
		t.Fatal("the root has no __transport command")
	}
	code, stdout, _ := execute("help", "--json")
	if code != 0 || strings.Contains(stdout, "__transport") {
		t.Errorf("help --json exit %d, want 0 without __transport in the document", code)
	}
}

func TestTransportKeepsStdoutEmptyWhereTheRootWouldPrint(t *testing.T) {
	for _, args := range [][]string{
		{"__transport", "--bad-flag"},
		{"__transport", "--json", "--bad-flag"},
		{"__transport", "--stdio"},
		{"__transport", "--help", "--json"},
	} {
		res := runBinary(t, t.TempDir(), "http://127.0.0.1:1", args...)
		if res.stdout != "" {
			t.Errorf("%q wrote %q to stdout, want nothing", args, res.stdout)
		}
	}
	res := runBinary(t, t.TempDir(), "http://127.0.0.1:1", "__transport", "--json", "--bad-flag")
	if res.exit != 2 || res.stderr == "" {
		t.Errorf("exit %d stderr %q, want exit 2 and a stderr message", res.exit, res.stderr)
	}
}

func TestTransportStdioKeepsRelayingAfterSIGINT(t *testing.T) {
	seat, err := localrun.NewSeat(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer seat.Close()
	var shim string
	for _, kv := range seat.Env() {
		if v, ok := strings.CutPrefix(kv, "SELECTIVE_TRANSPORT="); ok {
			shim = v
		}
	}
	cmd := exec.Command(shim, "--stdio")
	cmd.Env = append(append(os.Environ(), seat.Env()...), "SELECTIVE_TEST_RUN_MAIN=1")
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := seat.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(stdin, "{\"n\":1}\n")
	reader := bufio.NewReader(conn)
	if line, _ := reader.ReadString('\n'); line != "{\"n\":1}\n" {
		t.Fatalf("first relayed line = %q", line)
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	_, _ = io.WriteString(stdin, "{\"n\":2}\n")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if line, _ := reader.ReadString('\n'); line != "{\"n\":2}\n" {
		t.Errorf("line after SIGINT = %q, want the seat to keep relaying", line)
	}
	_, _ = io.WriteString(stdin, "exit\n")
	if err := cmd.Wait(); err != nil {
		t.Errorf("seat exit after exit line: %v, want 0", err)
	}
}
