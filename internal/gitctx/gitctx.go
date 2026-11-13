package gitctx

import (
	"fmt"
	"os/exec"
	"strings"
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

// Repo is the path of this checkout's origin remote: "owner/name" on
// GitHub, "group/subgroup/name" on GitLab.
func Repo() (string, error) { return gitRemoteSlug() }

// Origin is this checkout's origin remote: its path and, when the host says
// so, its provider ("github" or "gitlab"; empty when unknown).
func Origin() (path, provider string, err error) {
	out, err := run("git", "remote", "get-url", "origin")
	if err != nil {
		return "", "", err
	}
	url := strings.TrimSpace(out)
	path, err = parseSlug(url)
	return path, providerOf(url), err
}

func gitRemoteSlug() (string, error) {
	path, _, err := Origin()
	return path, err
}

func gitHEAD() (string, error) {
	out, err := run("git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s %v: %s", name, args, exitErr.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

// parseSlug extracts the repository path from an SSH or HTTPS remote URL:
// everything after the host, so GitLab's nested groups survive.
func parseSlug(url string) (string, error) {
	url = strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")

	// scp-like SSH: git@host:owner/repo
	if !strings.Contains(url, "://") {
		if at := strings.Index(url, "@"); at >= 0 {
			url = url[at+1:]
		}
		if parts := strings.SplitN(url, ":", 2); len(parts) == 2 && parts[1] != "" {
			return strings.Trim(parts[1], "/"), nil
		}
		return "", fmt.Errorf("cannot parse repo slug from %q", url)
	}

	// scheme://[user@]host[:port]/path
	rest := url[strings.Index(url, "://")+3:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", fmt.Errorf("cannot parse repo slug from %q", url)
	}
	path := strings.Trim(rest[slash+1:], "/")
	if strings.Count(path, "/") < 1 {
		return "", fmt.Errorf("cannot parse repo slug from %q", url)
	}
	return path, nil
}

// providerOf names the provider a remote's host belongs to. Self-managed
// GitLab is recognized only when "gitlab" is in its host name.
func providerOf(url string) string {
	host := url
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.Index(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	switch {
	case host == "github.com":
		return "github"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	}
	return ""
}
