package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	var unlock int32
	var number uint32
	for _, step := range []struct {
		request uintptr
		arg     unsafe.Pointer
	}{{syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)}, {syscall.TIOCGPTN, unsafe.Pointer(&number)}} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), step.request, uintptr(step.arg)); errno != 0 {
			t.Skipf("pty ioctl: %v", errno)
		}
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func (r selRun) runOnATerminal(t *testing.T, args ...string) cliResult {
	t.Helper()
	master, slave := openPTY(t)
	cmd := r.command(t, args...)
	cmd.Stdout = slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	out, _ := io.ReadAll(master)
	res := cliResult{stdout: strings.ReplaceAll(string(out), "\r\n", "\n")}
	var exitErr interface{ ExitCode() int }
	if err := cmd.Wait(); errors.As(err, &exitErr) {
		res.exit = exitErr.ExitCode()
	}
	return res
}

func TestAuthStatusOnATerminalIsTextUnlessJSONIsAsked(t *testing.T) {
	srv := meServer(t, 200, meBody)
	run := selRun{host: srv.URL, token: storedToken}

	pretty := run.runOnATerminal(t, "auth", "status")
	asked := run.runOnATerminal(t, "auth", "status", "--json")

	wantText := srv.URL + "\n  ✓ Logged in as ben@example.com\n  Organizations: acme, beta\n  Token: sk_live_abcdef… (from SELECTIVE_TOKEN)\n  Scopes: read setup\n"
	if pretty.exit != 0 || pretty.stdout != wantText {
		t.Errorf("terminal output = %q (exit %d), want %q", pretty.stdout, pretty.exit, wantText)
	}
	if asked.exit != 0 || decodeStatus(t, asked.stdout).Auth.Email != "ben@example.com" {
		t.Errorf("--json on a terminal = %q (exit %d), want the auth object", asked.stdout, asked.exit)
	}
}

func TestAuthStatusOnATerminalWhenNotLoggedInKeepsTheTextAndExitsFour(t *testing.T) {
	srv := meServer(t, 200, meBody)

	res := selRun{host: srv.URL}.runOnATerminal(t, "auth", "status")

	want := srv.URL + "\n  ✗ Not logged in. Run: sel auth login --hostname " + srv.URL + "\n"
	if res.exit != 4 || res.stdout != want {
		t.Errorf("terminal output = %q (exit %d), want %q and exit 4", res.stdout, res.exit, want)
	}
}

func TestAuthLogoutOnATerminalPrintsNothingToStdoutUnlessJSONIsAsked(t *testing.T) {
	stub := &revokeServer{status: 204}
	srv := stub.start(t)
	run := func() selRun { return selRun{host: srv.URL, configDir: storeInConfigFile(t, srv.URL)} }

	pretty := run().runOnATerminal(t, "auth", "logout")
	asked := run().runOnATerminal(t, "auth", "logout", "--json")

	if pretty.exit != 0 || pretty.stdout != "" {
		t.Errorf("terminal stdout = %q (exit %d), want nothing", pretty.stdout, pretty.exit)
	}
	want := `{"v":1,"auth":{"host":"` + srv.URL + `","revoked":true},"result":"deleted","dry_run":false}` + "\n"
	if asked.exit != 0 || asked.stdout != want {
		t.Errorf("--json on a terminal = %q (exit %d), want %q", asked.stdout, asked.exit, want)
	}
}
