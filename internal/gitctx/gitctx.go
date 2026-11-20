package gitctx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type Context struct {
	Repo   string // e.g. "selectiveci/selective"
	Commit string // 40-char SHA
}

func Detect() (*Context, error) {
	repo, err := gitRemoteSlug()
	if err != nil {
		return nil, fmt.Errorf("could not detect git repo: %w", err)
	}
	commit, err := gitHEAD()
	if err != nil {
		return nil, fmt.Errorf("could not detect HEAD commit: %w", err)
	}
	return &Context{Repo: repo, Commit: commit}, nil
}

// Repo is "owner/name" of this checkout's origin remote.
func Repo() (string, error) { return gitRemoteSlug() }

func gitRemoteSlug() (string, error) {
	out, err := Run("git", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return parseSlug(strings.TrimSpace(out))
}

func gitHEAD() (string, error) {
	out, err := Run("git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

var live = struct {
	sync.Mutex
	procs     map[*os.Process]struct{}
	abandoned bool
}{procs: map[*os.Process]struct{}{}}

func Abandon() {
	live.Lock()
	defer live.Unlock()
	live.abandoned = true
	for p := range live.procs {
		kill(p)
	}
}

func Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	isolate(cmd)
	if err := start(cmd); err != nil {
		return "", err
	}
	err := cmd.Wait()
	stop(cmd.Process)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%s %v: %s", name, args, stderr.Bytes())
		}
		return "", err
	}
	return out.String(), nil
}

func start(cmd *exec.Cmd) error {
	live.Lock()
	if live.abandoned {
		live.Unlock()
		select {}
	}
	defer live.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	live.procs[cmd.Process] = struct{}{}
	return nil
}

func stop(p *os.Process) {
	live.Lock()
	delete(live.procs, p)
	abandoned := live.abandoned
	live.Unlock()
	if abandoned {
		select {}
	}
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
