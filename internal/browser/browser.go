// Package browser opens a URL in the user's browser when there is one.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

// Open tries to open url and reports whether a browser was launched.
// It never fails the caller: over SSH, in a container or in CI there is no
// browser, and the URL printed alongside is the fallback.
func Open(url string) bool {
	cmd := command(url)
	if cmd == nil {
		return false
	}
	return cmd.Start() == nil
}

func command(url string) *exec.Cmd {
	if b := os.Getenv("BROWSER"); b != "" {
		return exec.Command(b, url)
	}
	switch runtime.GOOS {
	case "darwin":
		if os.Getenv("SSH_CONNECTION") != "" {
			return nil
		}
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		// No display, no browser.
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return nil
		}
		if path, err := exec.LookPath("xdg-open"); err == nil {
			return exec.Command(path, url)
		}
		return nil
	}
}
