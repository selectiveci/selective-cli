package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/mcpserver"
)

const (
	slowTermEnv       = "SEL_TEST_SLOW_TERM_CHILD"
	slowTermDelay     = 2 * time.Second
	gateIterations    = 5000
	ignoredCallSecond = 2
)

func init() {
	if ready := os.Getenv(slowTermEnv); ready != "" && len(os.Args) > 1 && os.Args[1] != "mcp" {
		os.Exit(slowTermChild(ready))
	}
}

func slowTermChild(ready string) int {
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM)
	_ = os.WriteFile(ready, nil, 0o600)
	<-terms
	time.Sleep(slowTermDelay)
	_ = os.WriteFile(ready+".finished", nil, 0o600)
	return 0
}

func startSelfAsSelMCP(t *testing.T, call string, extraEnv ...string) *shutdownRig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return launchSelMCP(t, exec.Command(exe, "mcp"), append([]string{"SELECTIVE_TEST_RUN_MAIN=1"}, extraEnv...), call)
}

func orgsCallWith(id, timeoutSeconds int) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"orgs","arguments":{"timeout_s":%d}}}`, id, timeoutSeconds)
}

func sigIgnHas(t *testing.T, pid int, sig syscall.Signal) bool {
	t.Helper()
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if mask, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			bits, err := strconv.ParseUint(strings.TrimSpace(mask), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return bits&(1<<(uint(sig)-1)) != 0
		}
	}
	t.Fatalf("no SigIgn line for pid %d", pid)
	return false
}

func exitCodeOfResponse(frame map[string]any) any {
	result, _ := frame["result"].(map[string]any)
	meta, _ := result["_meta"].(map[string]any)
	selective, _ := meta["selective"].(map[string]any)
	return selective["exit_code"]
}

func TestMCPShutdownWaitsForAChildThatTakesTimeToExitBeforeSelMCPExits(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	rig := startSelfAsSelMCP(t, orgsCall(), slowTermEnv+"="+ready)
	pgid := rig.liveChildGroup(t)
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("DID-NOT-RUN: the fake child never trapped SIGTERM: %v", err)
	}
	rig.signalAndWait(t, syscall.SIGTERM)
	assertGroupGone(t, pgid)
	if _, err := os.Stat(ready + ".finished"); err != nil {
		t.Errorf("the slow child did not finish its exit before sel mcp exited: %v", err)
	}
}

func startUnderTrap(t *testing.T, trap, call string) *shutdownRig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `trap "" `+trap+`; exec "$0" mcp`, exe)
	return launchSelMCP(t, cmd, []string{"SELECTIVE_TEST_RUN_MAIN=1"}, call)
}

func TestMCPShutdownLeavesAnInheritedSigintIgnoreIgnored(t *testing.T) {
	rig := startUnderTrap(t, "INT", orgsCallWith(9, ignoredCallSecond))
	rig.liveChildGroup(t)
	pid := rig.cmd.Process.Pid
	if !sigIgnHas(t, pid, syscall.SIGINT) {
		t.Errorf("sel mcp %d lost the inherited ignore of SIGINT at startup", pid)
	}
	if err := rig.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if got := exitCodeOfResponse(rig.stream.response(t, 9)); got != float64(8) {
		t.Errorf("in-flight call after the ignored SIGINT: exit_code = %v, want 8 (its own timeout)", got)
	}
	rig.stream.send(t, versionFrame)
	if got := exitCodeOfResponse(rig.stream.response(t, 3)); got != float64(0) {
		t.Errorf("call after the ignored SIGINT: exit_code = %v, want 0", got)
	}
	if !sigIgnHas(t, pid, syscall.SIGINT) {
		t.Errorf("sel mcp %d no longer ignores SIGINT", pid)
	}
	rig.stream.send(t, orgsCallWith(10, 30))
	pgid := rig.liveChildGroup(t)
	if status := rig.signalAndWait(t, syscall.SIGTERM); !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("wait status = %v, want killed by SIGTERM", status)
	}
	assertGroupGone(t, pgid)
}

func TestMCPShutdownSigtermStillEndsTheChildrenUnderAnInheritedTermTrap(t *testing.T) {
	rig := startUnderTrap(t, "TERM", orgsCall())
	pgid := rig.liveChildGroup(t)
	if status := rig.signalAndWait(t, syscall.SIGTERM); !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("wait status = %v, want killed by SIGTERM", status)
	}
	assertGroupGone(t, pgid)
}

func TestMCPRunnerRefusesEveryCallOnceShutdownHasBegun(t *testing.T) {
	runner, marker := markerRunner(t, mcpserver.Exposure{})
	if _, err := runner.Run(context.Background(), "version", json.RawMessage(`{}`)); err != nil || !ran(marker) {
		t.Fatalf("control: err = %v, ran = %v, want one spawn", err, ran(marker))
	}
	shutdown, cancel := context.WithCancel(context.Background())
	cancel()
	runner.WithShutdown(shutdown)
	for i := range gateIterations {
		_ = os.Remove(marker)
		if _, err := runner.Run(context.Background(), "version", json.RawMessage(`{}`)); err == nil || ran(marker) {
			t.Fatalf("call %d after shutdown: err = %v, ran = %v, want an error and no spawn", i, err, ran(marker))
		}
	}
}
