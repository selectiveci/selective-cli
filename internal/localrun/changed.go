package localrun

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
)

var defaultBaseCandidates = []string{"origin/main", "origin/master"}

func ChangedFiles(dir, against string) ([]string, error) {
	if _, ok := gitOutput(dir, "rev-parse", "--show-toplevel"); !ok {
		return nil, contract.Errorf(contract.UsageError, "sel test needs a git checkout to find what changed in %s", dir)
	}
	if against == "" {
		var found bool
		if against, found = defaultBase(dir); !found {
			return nil, contract.Errorf(contract.UsageError, "could not tell which branch to compare with (no origin/HEAD, origin/main or origin/master); pass --against REF")
		}
	}
	base, ok := gitOutput(dir, "merge-base", "HEAD", against)
	if !ok {
		return nil, contract.Errorf(contract.UsageError, "no merge base between HEAD and %q; pass --against REF with a branch this one grew from", against)
	}
	listing, ok := gitOutput(dir, "diff", "--name-only", "-z", "--no-renames", strings.TrimSpace(base))
	if !ok {
		return nil, contract.Errorf(contract.InternalError, "git diff against %s failed", strings.TrimSpace(base))
	}
	changed := []string{}
	for _, path := range strings.Split(listing, "\x00") {
		if path != "" {
			changed = append(changed, path)
		}
	}
	return changed, nil
}

func defaultBase(dir string) (string, bool) {
	if ref, ok := gitOutput(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); ok {
		return strings.TrimSpace(ref), true
	}
	for _, candidate := range defaultBaseCandidates {
		if _, ok := gitOutput(dir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); ok {
			return candidate, true
		}
	}
	return "", false
}

func RepoPaths(dir string, paths []string) ([]string, error) {
	prefix, ok := gitOutput(dir, "rev-parse", "--show-prefix")
	if !ok {
		return nil, contract.Errorf(contract.UsageError, "sel test needs a git checkout to place %s", strings.Join(paths, ", "))
	}
	placed := make([]string, len(paths))
	for i, p := range paths {
		joined := path.Join(strings.TrimSpace(prefix), filepath.ToSlash(p))
		if filepath.IsAbs(p) || joined == ".." || strings.HasPrefix(joined, "../") {
			return nil, contract.Errorf(contract.UsageError, "%s is outside this checkout; pass paths relative to the current directory", p)
		}
		placed[i] = joined
	}
	return placed, nil
}
