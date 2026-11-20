package gitctx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandTakesNoOptionalLocksAndKeepsTheParentEnvironment(t *testing.T) {
	shims := t.TempDir()
	seen := filepath.Join(t.TempDir(), "seen")
	shim := "#!/bin/sh\nprintf '%s|%s' \"$GIT_OPTIONAL_LOCKS\" \"$GITCTX_PARENT_VAR\" > \"$GITCTX_SEEN\"\n"
	if err := os.WriteFile(filepath.Join(shims, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITCTX_SEEN", seen)
	t.Setenv("GITCTX_PARENT_VAR", "kept")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")

	if err := Command(context.Background(), "status").Run(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the git shim never ran: %v", err)
	}
	if strings.TrimSpace(string(got)) != "0|kept" {
		t.Errorf("git saw GIT_OPTIONAL_LOCKS|GITCTX_PARENT_VAR = %q, want 0|kept", got)
	}
}
