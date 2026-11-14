package localrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func branchedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	for _, name := range []string{"app/a.rb", "app/b.rb", "app/c.rb", "lib/gone.rb"} {
		write(t, dir, name, "v1\n")
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	gitRun(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "app/a.rb", "v2\n")
	write(t, dir, "app/new.rb", "v1\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "feature work")
	gitRun(t, dir, "checkout", "-q", "main")
	write(t, dir, "app/moved_on_main.rb", "v1\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "main moves on")
	gitRun(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, dir, "checkout", "-q", "feature")
	return dir
}

func TestChangedFilesAreTheCommitsSinceTheMergeBasePlusTrackedEditsWithoutUntracked(t *testing.T) {
	dir := branchedRepo(t)
	write(t, dir, "app/b.rb", "edited, unstaged\n")
	write(t, dir, "app/c.rb", "edited, staged\n")
	gitRun(t, dir, "add", "app/c.rb")
	gitRun(t, dir, "rm", "-q", "lib/gone.rb")
	write(t, dir, "app/untracked.rb", "never added\n")

	got, err := ChangedFiles(dir, "")

	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app/a.rb", "app/b.rb", "app/c.rb", "app/new.rb", "lib/gone.rb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v (not the untracked file, not what main changed after the branch point)", got, want)
	}
}

func TestChangedFilesIsAnEmptyListWhenNothingChanged(t *testing.T) {
	dir := branchedRepo(t)
	gitRun(t, dir, "reset", "-q", "--hard", "origin/main")

	got, err := ChangedFiles(dir, "")

	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("changed = %#v err %v, want an empty, non-nil list", got, err)
	}
}

func TestChangedFilesAgainstAnExplicitRefUsesItsMergeBase(t *testing.T) {
	dir := branchedRepo(t)
	gitRun(t, dir, "tag", "feature-tip")
	gitRun(t, dir, "checkout", "-q", "-b", "more", "feature-tip")
	write(t, dir, "app/b.rb", "committed on more\n")
	gitRun(t, dir, "commit", "-q", "-am", "more")

	got, err := ChangedFiles(dir, "feature")

	if err != nil || !reflect.DeepEqual(got, []string{"app/b.rb"}) {
		t.Errorf("changed = %v err %v, want only the commit on top of feature", got, err)
	}
}

func TestChangedFilesDefaultBranchFallsBackToOriginMainThenFailsAsUsage(t *testing.T) {
	dir := branchedRepo(t)
	gitRun(t, dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	if got, err := ChangedFiles(dir, ""); err != nil || !reflect.DeepEqual(got, []string{"app/a.rb", "app/new.rb"}) {
		t.Errorf("with only origin/main: changed = %v err %v", got, err)
	}
	gitRun(t, dir, "update-ref", "-d", "refs/remotes/origin/main")

	_, err := ChangedFiles(dir, "")

	coded, ok := err.(contract.Coded)
	if !ok || coded.ErrorCode() != contract.UsageError || !strings.Contains(err.Error(), "--against") {
		t.Errorf("err = %v, want a usage_error that names --against", err)
	}
}

func TestChangedFilesRefusesAnUnknownRefAndANonCheckout(t *testing.T) {
	dir := branchedRepo(t)
	if _, err := ChangedFiles(dir, "no-such-ref"); contractCode(err) != contract.UsageError {
		t.Errorf("unknown ref: err = %v, want usage_error", err)
	}
	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside))
	if _, err := ChangedFiles(outside, ""); contractCode(err) != contract.UsageError {
		t.Errorf("outside a checkout: err = %v, want usage_error", err)
	}
}

func contractCode(err error) contract.Code {
	if coded, ok := err.(contract.Coded); ok {
		return coded.ErrorCode()
	}
	return ""
}

func TestChangedFilesFallsBackWhenOriginHeadPointsAtAMissingBranch(t *testing.T) {
	dir := branchedRepo(t)
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/renamed-away")

	got, err := ChangedFiles(dir, "")

	if err != nil || !reflect.DeepEqual(got, []string{"app/a.rb", "app/new.rb"}) {
		t.Errorf("changed = %v err %v, want the diff against origin/main", got, err)
	}
}

func TestChangedFilesAreRepoRootPathsEvenFromASubdirectoryWithDiffRelativeSet(t *testing.T) {
	dir := branchedRepo(t)
	gitRun(t, dir, "config", "diff.relative", "true")
	write(t, dir, "lib/gone.rb", "edited\n")

	got, err := ChangedFiles(filepath.Join(dir, "app"), "")

	want := []string{"app/a.rb", "app/new.rb", "lib/gone.rb"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v err %v, want %v", got, err, want)
	}
}

func TestChangedFilesIncludesASubmoduleChangeEvenWhenGitConfigIgnoresSubmodules(t *testing.T) {
	dir := branchedRepo(t)
	module := t.TempDir()
	gitRun(t, module, "init", "-q", "-b", "main")
	write(t, module, "m.txt", "v1\n")
	gitRun(t, module, "add", ".")
	gitRun(t, module, "commit", "-q", "-m", "module")
	gitRun(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", module, "vendored")
	gitRun(t, dir, "commit", "-q", "-m", "add the submodule")
	gitRun(t, dir, "config", "diff.ignoreSubmodules", "all")

	got, err := ChangedFiles(dir, "")

	if err != nil || !slices.Contains(got, "vendored") {
		t.Errorf("changed = %v err %v, want the submodule path among them", got, err)
	}
}

func TestChangedFilesDefaultBaseIsNotShadowedByALocalBranchNamedOriginMain(t *testing.T) {
	dir := branchedRepo(t)
	gitRun(t, dir, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	gitRun(t, dir, "branch", "origin/main", "HEAD")

	got, err := ChangedFiles(dir, "")

	if err != nil || !reflect.DeepEqual(got, []string{"app/a.rb", "app/new.rb"}) {
		t.Errorf("changed = %v err %v, want the diff against the remote-tracking origin/main", got, err)
	}
}
