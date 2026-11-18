package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pidRunning(t *testing.T, binary string) int {
	t.Helper()
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		argv, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil && strings.HasPrefix(string(argv), binary+"\x00mcp\x00") {
			var pid int
			_, _ = fmt.Sscanf(entry.Name(), "%d", &pid)
			return pid
		}
	}
	t.Fatalf("DID-NOT-RUN: no running %s mcp", binary)
	return 0
}

func awaitGroup(t *testing.T, label string, pgid int) {
	t.Helper()
	if pgid <= 1 || pgid == syscall.Getpgrp() || len(groupParents(pgid)) == 0 {
		t.Fatalf("DID-NOT-RUN: %s group %d is not alive and separate before the signal", label, pgid)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
}

func TestMCPStdioE2ESigtermDuringTheTestToolLeavesNeitherTheTestChildNorTheRunnerGroup(t *testing.T) {
	seed, binary := e2eSetup(t)
	venv := os.Getenv(e2eVenvVariable)
	if venv == "" {
		t.Fatalf("%s must name a virtualenv with the selective pytest client", e2eVenvVariable)
	}
	root, pidFile := signalIgnoringProject(t)
	session := startMCP(t, seed, binary, seed.WriteToken, root, []string{
		"PATH=" + filepath.Join(venv, "bin") + ":" + os.Getenv("PATH"),
		"VIRTUAL_ENV=" + venv,
		"E2E_IGNORER_FILE=" + pidFile,
	})
	go func() {
		_, _ = callTool(t, session, "test", map[string]any{"against": "HEAD", "timeout_s": 600, "suite": seed.RunSuite})
	}()
	deadline := time.Now().Add(60 * time.Second)
	for _, err := os.Stat(pidFile); err != nil && time.Now().Before(deadline); _, err = os.Stat(pidFile) {
		time.Sleep(50 * time.Millisecond)
	}
	var runnerPid, runnerGroup int
	if body, err := os.ReadFile(pidFile); err != nil {
		t.Fatalf("DID-NOT-RUN: the signal-ignoring runner never started: %v", err)
	} else if _, err := fmt.Sscanf(string(body), "%d %d", &runnerPid, &runnerGroup); err != nil {
		t.Fatal(err)
	}
	mcpPid := pidRunning(t, binary)
	testChild, ok := childOf(mcpPid)
	if !ok {
		t.Fatalf("DID-NOT-RUN: sel mcp %d has no sel test child", mcpPid)
	}
	testGroup, err := syscall.Getpgid(testChild)
	if err != nil {
		t.Fatal(err)
	}
	awaitGroup(t, "sel test", testGroup)
	awaitGroup(t, "runner", runnerGroup)
	t.Logf("did-it-run: sel test child %d group %d and runner %d group %d alive under sel mcp %d", testChild, testGroup, runnerPid, runnerGroup, mcpPid)
	if err := syscall.Kill(mcpPid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(x14Bound); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if len(groupParents(testGroup))+len(groupParents(runnerGroup)) == 0 {
			return
		}
	}
	t.Errorf("within %s of SIGTERM, sel test group %d has %v and runner group %d has %v", x14Bound, testGroup, groupParents(testGroup), runnerGroup, groupParents(runnerGroup))
}
