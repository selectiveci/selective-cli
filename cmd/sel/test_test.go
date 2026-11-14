package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cmd := exec.Command(os.Args[0], "__transport", "--stdio")
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_SEAT_SOCKET="+sock)
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	ln.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(stdin, "{\"n\":1}\n")
	reader := bufio.NewReader(conn)
	if line, _ := reader.ReadString('\n'); line != "{\"n\":1}\n" {
		t.Fatalf("first relayed line = %q", line)
	}
	cmd.Process.Signal(syscall.SIGINT)
	cmd.Process.Signal(syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	io.WriteString(stdin, "{\"n\":2}\n")
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if line, _ := reader.ReadString('\n'); line != "{\"n\":2}\n" {
		t.Errorf("line after SIGINT = %q, want the seat to keep relaying", line)
	}
	io.WriteString(stdin, "exit\n")
	if err := cmd.Wait(); err != nil {
		t.Errorf("seat exit after exit line: %v, want 0", err)
	}
}
