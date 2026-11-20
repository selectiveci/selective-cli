package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestSelTestWatchInterruptedWhileGitIsRunningExitsInterruptedNotInternalError(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, started, path := t.TempDir(), filepath.Join(t.TempDir(), "ls-tree.pid"), os.Getenv("PATH")
	script := "#!/bin/sh\ncase \" $* \" in *\" ls-tree \"*) echo $$ > " + started + "; exec sleep 300;; esac\nPATH='" + path + "'\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+path)
	w := startWatch(t, newFakeAPI(t, selected))
	var gitPid int
	t.Cleanup(func() {
		if gitPid > 0 {
			_ = syscall.Kill(gitPid, syscall.SIGKILL)
		}
	})
	waitUntil(t, "the fingerprint's git ls-tree to start", func() bool {
		body, err := os.ReadFile(started)
		if err != nil {
			return false
		}
		gitPid, err = strconv.Atoi(strings.TrimSpace(string(body)))
		return err == nil
	})

	_, code := w.interrupt()

	if code != 130 || !processGone(gitPid) {
		t.Errorf("exit %d, git pid %d gone=%t; want 130 for a SIGINT that landed while git was running, and that git dead", code, gitPid, processGone(gitPid))
	}
}
