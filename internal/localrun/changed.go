package localrun

import (
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
)

var defaultBaseCandidates = []string{"refs/remotes/origin/main", "refs/remotes/origin/master"}

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
	listing, ok := gitOutput(dir, "-c", "diff.relative=false", "diff", "--ignore-submodules=none", "--name-only", "-z", "--no-renames", strings.TrimSpace(base))
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
	candidates := defaultBaseCandidates
	if ref, ok := gitOutput(dir, "symbolic-ref", "refs/remotes/origin/HEAD"); ok {
		candidates = append([]string{strings.TrimSpace(ref)}, candidates...)
	}
	for _, candidate := range candidates {
		if _, ok := gitOutput(dir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); ok {
			return candidate, true
		}
	}
	return "", false
}
