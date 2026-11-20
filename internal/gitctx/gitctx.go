package gitctx

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Context struct {
	Repo   string // e.g. "selectiveci/selective"
	Commit string // 40-char SHA
}

func Detect(ctx context.Context) (*Context, error) {
	repo, err := gitRemoteSlug(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not detect git repo: %w", err)
	}
	commit, err := gitHEAD(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not detect HEAD commit: %w", err)
	}
	return &Context{Repo: repo, Commit: commit}, nil
}

// Repo is "owner/name" of this checkout's origin remote.
func Repo(ctx context.Context) (string, error) { return gitRemoteSlug(ctx) }

func gitRemoteSlug(ctx context.Context) (string, error) {
	out, err := run(ctx, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return parseSlug(strings.TrimSpace(out))
}

func gitHEAD(ctx context.Context) (string, error) {
	out, err := run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd
}

func run(ctx context.Context, args ...string) (string, error) {
	cmd := Command(ctx, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %v: %s", args, exitErr.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

// parseSlug extracts "owner/repo" from HTTPS or SSH remote URLs.
func parseSlug(url string) (string, error) {
	url = strings.TrimSuffix(url, ".git")

	// SSH: git@github.com:owner/repo
	if strings.HasPrefix(url, "git@") {
		parts := strings.SplitN(url, ":", 2)
		if len(parts) == 2 {
			return parts[1], nil
		}
	}

	// HTTPS: https://github.com/owner/repo
	if strings.Contains(url, "github.com/") {
		idx := strings.Index(url, "github.com/")
		return url[idx+len("github.com/"):], nil
	}

	// Fallback: assume last two path components
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/"), nil
	}
	return "", fmt.Errorf("cannot parse repo slug from %q", url)
}
