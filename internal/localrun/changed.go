package localrun

import (
	"context"
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
)

var defaultBaseCandidates = []string{"refs/remotes/origin/main", "refs/remotes/origin/master"}

func ChangedFiles(ctx context.Context, dir, against string) ([]string, error) {
	if _, ok := gitOutput(ctx, dir, "rev-parse", "--show-toplevel"); !ok {
		return nil, contract.Errorf(contract.GitRepoNotFound, "sel test needs a git checkout to find what changed in %s", dir)
	}
	if against == "" {
		var found bool
		if against, found = defaultBase(ctx, dir); !found {
			return nil, contract.Errorf(contract.UsageError, "could not tell which branch to compare with (no origin/HEAD, origin/main or origin/master); pass --against REF")
		}
	}
	if strings.HasPrefix(against, "-") {
		return nil, contract.Errorf(contract.UsageError, "--against %q looks like an option, not a ref", against)
	}
	base, ok := gitOutput(ctx, dir, "merge-base", "HEAD", against)
	if !ok {
		return nil, contract.Errorf(contract.UsageError, "no merge base between HEAD and %q; pass --against REF with a branch this one grew from", against)
	}
	listing, ok := gitOutput(ctx, dir, "-c", "diff.relative=false", "diff", "--ignore-submodules=none", "--name-only", "-z", "--no-renames", strings.TrimSpace(base))
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

func defaultBase(ctx context.Context, dir string) (string, bool) {
	candidates := defaultBaseCandidates
	if ref, ok := gitOutput(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD"); ok {
		candidates = append([]string{strings.TrimSpace(ref)}, candidates...)
	}
	for _, candidate := range candidates {
		if _, ok := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", candidate+"^{commit}"); ok {
			return candidate, true
		}
	}
	return "", false
}
