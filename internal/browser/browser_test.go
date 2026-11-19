package browser

import (
	"errors"
	"slices"
	"testing"
)

const url = "https://app.selective.ci/device?code=ABCD-EFGH&signup=1"

func TestCommand(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		bins []string
		want []string
	}{
		{"macOS", "darwin", nil, nil, []string{"open", url}},
		{"windows", "windows", nil, nil, []string{"rundll32", "url.dll,FileProtocolHandler", url}},
		{"linux desktop", "linux", map[string]string{"DISPLAY": ":0"}, []string{"xdg-open"}, []string{"/bin/xdg-open", url}},
		{"wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, []string{"xdg-open"}, []string{"/bin/xdg-open", url}},
		{"linux without a display", "linux", nil, []string{"xdg-open"}, nil},
		{"linux without xdg-open", "linux", map[string]string{"DISPLAY": ":0"}, nil, nil},
		{"freebsd desktop", "freebsd", map[string]string{"DISPLAY": ":0"}, []string{"xdg-open"}, []string{"/bin/xdg-open", url}},
		{"WSL", "linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, []string{"wslview"}, []string{"/bin/wslview", url}},
		{"WSL without wslview", "linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "DISPLAY": ":0"}, []string{"xdg-open"}, []string{"/bin/xdg-open", url}},
		{"opted out", "darwin", map[string]string{"SELECTIVE_NO_BROWSER": "1"}, nil, nil},
		{"CI", "darwin", map[string]string{"CI": "true"}, nil, nil},
		{"GitHub Actions", "linux", map[string]string{"GITHUB_ACTIONS": "true", "DISPLAY": ":0"}, []string{"xdg-open"}, nil},
		{"SSH into a Mac", "darwin", map[string]string{"SSH_CONNECTION": "1.2.3.4 22 5.6.7.8 22"}, nil, nil},
		{"SSH with X forwarding", "linux", map[string]string{"SSH_CLIENT": "1.2.3.4 22 22", "DISPLAY": "localhost:10.0"}, []string{"xdg-open"}, nil},
		{"SSH into Windows", "windows", map[string]string{"SSH_TTY": "/dev/pts/0"}, nil, nil},
		{"$BROWSER", "linux", map[string]string{"BROWSER": "firefox"}, []string{"firefox"}, []string{"/bin/firefox", url}},
		{"$BROWSER over SSH", "linux", map[string]string{"BROWSER": "firefox", "SSH_TTY": "/dev/pts/0"}, []string{"firefox"}, []string{"/bin/firefox", url}},
		{"$BROWSER not installed", "darwin", map[string]string{"BROWSER": "none"}, nil, nil},
		{"$BROWSER in CI", "linux", map[string]string{"BROWSER": "firefox", "CI": "1"}, []string{"firefox"}, nil},
		{"Codespaces", "linux", map[string]string{"CODESPACES": "true", "BROWSER": "helper"}, []string{"helper"}, []string{"/bin/helper", url}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			lookPath := func(name string) (string, error) {
				if slices.Contains(tc.bins, name) {
					return "/bin/" + name, nil
				}
				return "", errors.New("not found")
			}
			if got := Command(url, tc.goos, getenv, lookPath); !slices.Equal(got, tc.want) {
				t.Errorf("Command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenReportsAFailedLaunch(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("BUILD_NUMBER", "")
	t.Setenv("RUN_ID", "")
	t.Setenv(OptOutVariable, "")
	t.Setenv("BROWSER", "/nonexistent/browser")
	if Open(url) {
		t.Error("Open = true for a browser that isn't installed")
	}
}

// A real launch, with a stand-in browser that records its argument.
func TestOpenLaunchesDetached(t *testing.T) {
	for _, name := range append(append([]string{OptOutVariable}, ciVariables...), sshVariables...) {
		t.Setenv(name, "")
	}
	t.Setenv("BROWSER", "true")
	if !Open(url) {
		t.Error("Open = false with $BROWSER=true")
	}
}
