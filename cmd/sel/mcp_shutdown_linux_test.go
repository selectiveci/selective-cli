package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/localrun"
)

const (
	terminateGrace = localrun.ShutdownBudget + time.Second
	shutdownBound  = terminateGrace + time.Second
)

type shutdownRig struct {
	cmd    *exec.Cmd
	done   chan error
	stream frameStream
}

func startSelMCP(t *testing.T, call string) *shutdownRig {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "sel")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return launchSelMCP(t, exec.Command(binary, "mcp"), nil, call)
}

func launchSelMCP(t *testing.T, cmd *exec.Cmd, extraEnv []string, call string) *shutdownRig {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		var held []net.Conn
		for {
			conn, err := listener.Accept()
			if err != nil {
				for _, c := range held {
					_ = c.Close()
				}
				return
			}
			held = append(held, conn)
		}
	}()
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "SELECTIVE_CONFIG_DIR="+t.TempDir(), "SELECTIVE_HOST=http://"+listener.Addr().String(),
		"SELECTIVE_TOKEN=dummy", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent")
	cmd.Env = append(cmd.Env, extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	stream := frameStream{in: stdin, out: bufio.NewReader(stdout)}
	handshake(t, stream)
	if call != "" {
		stream.send(t, call)
	}
	rig := &shutdownRig{cmd: cmd, done: make(chan error, 1), stream: stream}
	go func() { rig.done <- cmd.Wait() }()
	return rig
}

func orgsCall() string {
	return `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"orgs","arguments":{"timeout_s":30}}}`
}

func childOf(parent int) (int, bool) {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) > 2 && fields[0] != "Z" && fields[1] == strconv.Itoa(parent) {
			return pid, true
		}
	}
	return 0, false
}

func groupParents(pgid int) map[int]string {
	entries, _ := os.ReadDir("/proc")
	members := map[int]string{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) > 2 && fields[0] != "Z" && fields[2] == strconv.Itoa(pgid) {
			members[pid] = "ppid " + fields[1]
		}
	}
	return members
}

func (r *shutdownRig) liveChildGroup(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if child, ok := childOf(r.cmd.Process.Pid); ok {
			pgid, err := syscall.Getpgid(child)
			if err != nil || pgid != child || pgid == 1 {
				t.Fatalf("child %d pgid = %d (%v), want its own group", child, pgid, err)
			}
			t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
			t.Logf("did-it-run: child %d is its own group, ppid %d = sel mcp", child, r.cmd.Process.Pid)
			return pgid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("DID-NOT-RUN: sel mcp %d has no tool child", r.cmd.Process.Pid)
	return 0
}

func (r *shutdownRig) signalAndWait(t *testing.T, sig syscall.Signal) syscall.WaitStatus {
	t.Helper()
	ignored, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", r.cmd.Process.Pid))
	for _, line := range strings.Split(string(ignored), "\n") {
		if strings.HasPrefix(line, "SigIgn") {
			t.Logf("sel mcp %s", line)
		}
	}
	if err := r.cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
	case <-time.After(shutdownBound):
		t.Fatalf("sel mcp still running %s after %v", shutdownBound, sig)
	}
	status := r.cmd.ProcessState.Sys().(syscall.WaitStatus)
	t.Logf("sel mcp wait status after %v: %v signaled=%v signal=%v exit=%d", sig, r.cmd.ProcessState, status.Signaled(), status.Signal(), status.ExitStatus())
	return status
}

func assertGroupGone(t *testing.T, pgid int) {
	t.Helper()
	if left := groupParents(pgid); len(left) > 0 {
		t.Errorf("sel mcp is gone but process group %d still has %v", pgid, left)
	}
}

func TestMCPShutdownSignalEndsTheInFlightChildsGroupWithinTheGrace(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			rig := startSelMCP(t, orgsCall())
			pgid := rig.liveChildGroup(t)
			rig.signalAndWait(t, sig)
			assertGroupGone(t, pgid)
		})
	}
}

func TestMCPShutdownKeepsSelMCPsOwnWaitStatusAsTheSignalThatKilledIt(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		for _, midCall := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/midCall=%v", sig, midCall), func(t *testing.T) {
				call := ""
				if midCall {
					call = orgsCall()
				}
				rig := startSelMCP(t, call)
				if midCall {
					rig.liveChildGroup(t)
				}
				if status := rig.signalAndWait(t, sig); !status.Signaled() || status.Signal() != sig {
					t.Errorf("wait status = %v, want killed by %v", status, sig)
				}
			})
		}
	}
}
