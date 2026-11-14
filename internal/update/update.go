package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/selectiveci/selective-cli/internal/config"
	"golang.org/x/mod/semver"
)

const (
	LatestHeader   = "X-Selective-Cli-Latest-Version"
	MinHeader      = "X-Selective-Cli-Min-Version"
	UpgradeCommand = "curl -fsSL https://raw.githubusercontent.com/selectiveci/selective-cli/main/install.sh | sh"
	releaseTagURL  = "https://github.com/selectiveci/selective-cli/releases/tag/v"
	cacheFile      = "update-check.json"
)

var Version = "dev"

type State struct {
	LatestVersion       string     `json:"latest_version,omitempty"`
	CheckedAt           *time.Time `json:"checked_at,omitempty"`
	ServerLatestVersion string     `json:"server_latest_version,omitempty"`
	ServerMinVersion    string     `json:"server_min_version,omitempty"`
	ServerSeenAt        *time.Time `json:"server_seen_at,omitempty"`
}

type Info struct {
	Version         string     `json:"version"`
	Commit          string     `json:"commit"`
	UpdateAvailable *bool      `json:"update_available,omitempty"`
	LatestVersion   string     `json:"latest_version,omitempty"`
	MinVersion      string     `json:"min_version,omitempty"`
	BelowMinimum    *bool      `json:"below_minimum,omitempty"`
	URL             string     `json:"url,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	UpgradeCommand  string     `json:"upgrade_command,omitempty"`
}

func UserAgent() string {
	return fmt.Sprintf("sel/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH)
}

func ReleaseURL(version string) string {
	return releaseTagURL + version
}

func cleanVersion(raw string) string {
	v := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if v == "" || !semver.IsValid("v"+v) {
		return ""
	}
	return v
}

func isSemver(version string) bool {
	return cleanVersion(version) != ""
}

func statePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheFile), nil
}

func LoadState() State {
	var st State
	path, err := statePath()
	if err != nil {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if json.Unmarshal(data, &st) != nil {
		return State{}
	}
	return st
}

func saveState(st State) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, cacheFile+".*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func now() *time.Time {
	t := time.Now().UTC().Truncate(time.Second)
	return &t
}

func RecordServerHeaders(h http.Header) {
	if !isSemver(Version) {
		return
	}
	latest := cleanVersion(h.Get(LatestHeader))
	minimum := cleanVersion(h.Get(MinHeader))
	if latest == "" && minimum == "" {
		return
	}
	st := LoadState()
	if latest != "" {
		st.ServerLatestVersion = latest
	}
	if minimum != "" {
		st.ServerMinVersion = minimum
	}
	st.ServerSeenAt = now()
	_ = saveState(st)
}

type transport struct {
	base http.RoundTripper
}

func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return transport{base}
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", UserAgent())
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		RecordServerHeaders(resp.Header)
	}
	return resp, err
}

func Status(version, commit string, st State) Info {
	info := Info{Version: version, Commit: commit}
	current := cleanVersion(version)
	if current == "" {
		return info
	}
	latest, checkedAt := cleanVersion(st.LatestVersion), st.CheckedAt
	if latest == "" {
		latest, checkedAt = cleanVersion(st.ServerLatestVersion), st.ServerSeenAt
	}
	outdated := false
	if latest != "" {
		outdated = semver.Compare("v"+current, "v"+latest) < 0
		info.UpdateAvailable = &outdated
		info.LatestVersion = latest
		info.URL = ReleaseURL(latest)
		info.CheckedAt = checkedAt
	}
	unsupported := false
	if minimum := cleanVersion(st.ServerMinVersion); minimum != "" {
		unsupported = semver.Compare("v"+current, "v"+minimum) < 0
		info.BelowMinimum = &unsupported
		info.MinVersion = minimum
	}
	if outdated || unsupported {
		info.UpgradeCommand = UpgradeCommand
	}
	return info
}
