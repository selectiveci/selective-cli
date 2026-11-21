package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSuitesDeleteTimeoutCountsTheSetupThatRunsBeforeTheFirstRequest(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	if err := syscall.Mkfifo(configPath, 0o600); err != nil {
		t.Fatal(err)
	}
	const setupStall = 1500 * time.Millisecond
	go func() {
		time.Sleep(setupStall)
		f, err := os.OpenFile(configPath, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.WriteString("{}")
	}()
	stub := &deleteStub{replies: []deleteReply{deleteDone}}
	srv := stub.start(t)

	began := time.Now()
	res := runBinaryIn(t, t.TempDir(), srv.URL, configDir, "suites", "delete", "old-minitest", "--yes", "--timeout=500ms", "--org", "acme", "--json")

	if took := time.Since(began); took >= setupStall {
		t.Fatalf("took %s, the exit waited for the %s config read instead of ending at the --timeout bound", took, setupStall)
	}
	assertRefusal(t, res, 8, "timeout")
	if n := stub.requestCount(); n != 0 {
		t.Errorf("%d requests reached the server, want none once --timeout had expired during setup", n)
	}
}
