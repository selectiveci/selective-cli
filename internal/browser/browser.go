// Package browser opens a URL in the user's browser when there is one.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

const OptOutVariable = "SELECTIVE_NO_BROWSER"

// CODESPACES is not here: Codespaces sets $BROWSER to a helper that opens
// the page on the person's own machine.
var ciVariables = []string{"CI", "GITHUB_ACTIONS", "BUILD_NUMBER", "RUN_ID"}

var sshVariables = []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"}

// Open tries to open url and reports whether a browser was launched.
// The launch is decided by the environment, not by whether stdio is a
// terminal: an agent's shell pipes both, yet a person is at the screen.
func Open(url string) bool {
	argv := Command(url, runtime.GOOS, os.Getenv, exec.LookPath)
	if argv == nil {
		return false
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// Nil stdio is /dev/null, so the opener can't hold an agent's output
	// pipe open after sel exits.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}

// Command returns the argv that opens url on goos, or nil when there is no
// browser to open: opted out, CI, SSH, or no display.
func Command(url, goos string, getenv func(string) string, lookPath func(string) (string, error)) []string {
	if getenv(OptOutVariable) != "" || anySet(getenv, ciVariables) {
		return nil
	}
	if b := getenv("BROWSER"); b != "" {
		if path, err := lookPath(b); err == nil {
			return []string{path, url}
		}
		return nil
	}
	if anySet(getenv, sshVariables) {
		return nil
	}
	switch goos {
	case "darwin":
		return []string{"open", url}
	case "windows":
		// Not `cmd /c start`: cmd treats the & between query parameters as
		// a command separator.
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}
	}
	if getenv("WSL_DISTRO_NAME") != "" {
		if path, err := lookPath("wslview"); err == nil {
			return []string{path, url}
		}
	}
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return nil
	}
	if path, err := lookPath("xdg-open"); err == nil {
		return []string{path, url}
	}
	return nil
}

func anySet(getenv func(string) string, names []string) bool {
	for _, name := range names {
		if getenv(name) != "" {
			return true
		}
	}
	return false
}
