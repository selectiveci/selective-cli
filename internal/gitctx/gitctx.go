package gitctx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd
}

var ErrKilled = errors.New("git was stopped because sel is exiting")

var running = struct {
	sync.Mutex
	cmds   map[*exec.Cmd]struct{}
	killed bool
}{cmds: map[*exec.Cmd]struct{}{}}

func Run(cmd *exec.Cmd) error {
	running.Lock()
	if running.killed {
		running.Unlock()
		return ErrKilled
	}
	err := cmd.Start()
	if err == nil {
		running.cmds[cmd] = struct{}{}
	}
	running.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		running.Lock()
		delete(running.cmds, cmd)
		running.Unlock()
	}()
	return cmd.Wait()
}

const stderrKept = 32 << 10

type stderrSaver struct {
	head, tail []byte
	omitted    int64
}

func (s *stderrSaver) Write(p []byte) (int, error) {
	n := len(p)
	if room := stderrKept - len(s.head); room > 0 {
		kept := min(room, len(p))
		s.head, p = append(s.head, p[:kept]...), p[kept:]
	}
	s.tail = append(s.tail, p...)
	if over := len(s.tail) - stderrKept; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
		s.omitted += int64(over)
	}
	return n, nil
}

func (s *stderrSaver) Bytes() []byte {
	if s.omitted == 0 {
		return append(s.head, s.tail...)
	}
	return fmt.Appendf(nil, "%s\n... omitting %d bytes ...\n%s", s.head, s.omitted, s.tail)
}

func Output(cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr stderrSaver
	cmd.Stdout = &stdout
	captureStderr := cmd.Stderr == nil
	if captureStderr {
		cmd.Stderr = &stderr
	}
	err := Run(cmd)
	var exitErr *exec.ExitError
	if captureStderr && errors.As(err, &exitErr) {
		exitErr.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

func KillRunning() {
	running.Lock()
	defer running.Unlock()
	running.killed = true
	for cmd := range running.cmds {
		_ = cmd.Process.Kill()
	}
}

func run(ctx context.Context, args ...string) (string, error) {
	cmd := Command(ctx, args...)
	out, err := Output(cmd)
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
