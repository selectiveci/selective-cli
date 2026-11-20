package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTimeoutCapLeavesNoGitChildBehindWhenDetectionHangs(t *testing.T) {
	bin, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "git.pid")
	killRecordedPidAtCleanup(t, pidFile)
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res, _ := runBinaryWithin(t, 5*time.Second, t.TempDir(), t.TempDir(), []string{"PATH=" + bin + ":" + os.Getenv("PATH")}, "tail", "--timeout=1s", "--json")

	pid := recordedPid(t, pidFile)
	if res.exit != 8 || strings.Count(res.stdout, "\n") != 1 || !processGone(pid) {
		t.Errorf("exit %d stdout %q, git pid %d gone=%t; want exit 8, one timeout line and no live git", res.exit, res.stdout, pid, processGone(pid))
	}
}

func TestTimeoutLeavesNoGitChildWhenDoctorsOwnDeadlineFiresFirst(t *testing.T) {
	cleanDoctorEnv(t)
	srv := doctorServer(t, 200, meGreen)
	dir := checkoutOfAcmeWeb(t)
	bin, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "git.pid")
	killRecordedPidAtCleanup(t, pidFile)
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	res := runBinary(t, dir, srv.URL, "doctor", "--timeout", "2s", "--json")

	pid := recordedPid(t, pidFile)
	if res.exit != 8 || !processGone(pid) {
		t.Errorf("exit %d, git pid %d gone=%t; want exit 8 and no live git", res.exit, pid, processGone(pid))
	}
}

func TestTimeoutLeavesNoGrandchildOfGitBehind(t *testing.T) {
	bin, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "sleep.pid")
	killRecordedPidAtCleanup(t, pidFile)
	script := "#!/bin/sh\nsleep 300 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res, _ := runBinaryWithin(t, 5*time.Second, t.TempDir(), t.TempDir(), []string{"PATH=" + bin + ":" + os.Getenv("PATH")}, "tail", "--timeout=1s", "--json")

	pid := recordedPid(t, pidFile)
	if res.exit != 8 || !processGone(pid) {
		t.Errorf("exit %d, grandchild pid %d gone=%t; want exit 8 and no live grandchild", res.exit, pid, processGone(pid))
	}
}
