package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
)

const (
	approvalObject = `{"expires_at":"2026-11-16T00:00:00Z","id":"a1","status":"pending","url":"https://selective.test/acme/approvals/a1"}`
	pendingWrite   = `{"result":"pending_approval","dry_run":false,"approval":` + approvalObject + `}`
	pendingStdout  = `{"v":1,"result":"pending_approval","dry_run":false,"approval":` + approvalObject + `}` + "\n"
)

func TestRevokeAnsweredWithAPendingApprovalExitsSevenAndPrintsOnlyTheWriteObject(t *testing.T) {
	stub := &revokeStub{status: 202, body: pendingWrite}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--yes", "--json")

	if res.exit != 7 || res.stdout != pendingStdout || strings.Contains(res.stdout, `"token"`) {
		t.Errorf("exit %d stdout %q, want exit 7 and exactly %q", res.exit, res.stdout, pendingStdout)
	}
}

func TestAGatedDryRunExitsSevenWithDryRunTrueAndNoApproval(t *testing.T) {
	stub := &revokeStub{status: 200, body: `{"result":"pending_approval","dry_run":true}`}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "revoke", revokeID, "--dry-run", "--json")

	if want := `{"v":1,"result":"pending_approval","dry_run":true}` + "\n"; res.exit != 7 || res.stdout != want {
		t.Errorf("exit %d stdout %q, want exit 7 and %q", res.exit, res.stdout, want)
	}
}

func TestCreateAnsweredWithAPendingApprovalClaimsNoTokenAndWritesNoFile(t *testing.T) {
	stub := &mintStub{status: 202, body: pendingWrite}
	srv := stub.start(t)
	path := filepath.Join(t.TempDir(), "ci.token")

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "create", "--name", "ci", "--agent", "--output", path, "--json")

	if _, err := os.Stat(path); err == nil {
		t.Error("the --output file was created")
	}
	if res.exit != 7 || res.stdout != pendingStdout || strings.Contains(res.stdout+res.stderr, "was created") {
		t.Errorf("exit %d stdout %q stderr %q, want exit 7, the pending object and no claim of a token", res.exit, res.stdout, res.stderr)
	}
}

func TestPendingApprovalFromCreateStdoutGoesToStderrAndLeavesStdoutEmpty(t *testing.T) {
	stub := &mintStub{status: 202, body: pendingWrite}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tokens", "create", "--name", "ci", "--agent", "--stdout")

	if res.exit != 7 || res.stdout != "" || !strings.Contains(res.stderr, pendingStdout) {
		t.Errorf("exit %d stdout %q stderr %q, want exit 7, empty stdout and the object on stderr", res.exit, res.stdout, res.stderr)
	}
}

func TestPendingSentenceNamesTheApprovalOrSaysNothingWasRequested(t *testing.T) {
	asked := &api.PendingApproval{Approval: map[string]any{"url": "https://selective.test/acme/approvals/a1", "expires_at": "2026-11-16T00:00:00Z"}}
	dry := &api.PendingApproval{DryRun: true}

	if got, want := pendingSentence(asked), "Approval needed: https://selective.test/acme/approvals/a1 (expires 2026-11-16 00:00 UTC). Run the same command again once it is approved."; got != want {
		t.Errorf("sentence = %q, want %q", got, want)
	}
	if got, want := pendingSentence(dry), "Dry run: this needs a person's approval; nothing was requested."; got != want {
		t.Errorf("dry sentence = %q, want %q", got, want)
	}
}
