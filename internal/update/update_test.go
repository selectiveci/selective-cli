package update

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolated(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SELECTIVE_CONFIG_DIR", dir)
	previous := Version
	Version = version
	t.Cleanup(func() { Version = previous })
	return dir
}

func headers(latest, minimum string) http.Header {
	h := http.Header{}
	if latest != "" {
		h.Set(LatestHeader, latest)
	}
	if minimum != "" {
		h.Set(MinHeader, minimum)
	}
	return h
}

func TestServerHeadersAreCachedOwnerReadableOnly(t *testing.T) {
	dir := isolated(t, "0.1.0")

	RecordServerHeaders(headers("v0.3.0", "0.2.0"))

	st := LoadState()
	if st.ServerLatestVersion != "0.3.0" || st.ServerMinVersion != "0.2.0" || st.ServerSeenAt == nil {
		t.Errorf("state = %+v", st)
	}
	info, err := os.Stat(filepath.Join(dir, "update-check.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("cache file: %v, mode %v; want 0600", err, info)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestAbsentOrUnusableHeadersDoNotTouchTheCache(t *testing.T) {
	cases := map[string]http.Header{
		"no headers": headers("", ""),
		"not semver": headers("soon", "<script>"),
		"empty":      headers(" ", " "),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			dir := isolated(t, "0.1.0")

			RecordServerHeaders(h)

			if _, err := os.Stat(filepath.Join(dir, "update-check.json")); !os.IsNotExist(err) {
				t.Errorf("cache file exists (stat err = %v), want none", err)
			}
		})
	}
}

func TestLaterResponseWithoutHeadersKeepsCachedValues(t *testing.T) {
	isolated(t, "0.1.0")
	RecordServerHeaders(headers("0.3.0", "0.2.0"))

	RecordServerHeaders(headers("0.4.0", ""))
	RecordServerHeaders(headers("", ""))

	st := LoadState()
	if st.ServerLatestVersion != "0.4.0" || st.ServerMinVersion != "0.2.0" {
		t.Errorf("state = %+v, want latest 0.4.0 and min 0.2.0 kept", st)
	}
}

func TestDevBuildsRecordNothing(t *testing.T) {
	dir := isolated(t, "dev")

	RecordServerHeaders(headers("0.3.0", "0.2.0"))

	if _, err := os.Stat(filepath.Join(dir, "update-check.json")); !os.IsNotExist(err) {
		t.Errorf("dev build wrote a cache file (stat err = %v)", err)
	}
}

func TestTransportSendsUserAgentAndRecordsHeaders(t *testing.T) {
	isolated(t, "0.1.0")
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		w.Header().Set(LatestHeader, "0.3.0")
	}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("User-Agent", "other/1")

	resp, err := (&http.Client{Transport: Transport(nil)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasPrefix(seen, "sel/0.1.0 (") || req.Header.Get("User-Agent") != "other/1" {
		t.Errorf("server saw %q, caller's request now has %q", seen, req.Header.Get("User-Agent"))
	}
	if LoadState().ServerLatestVersion != "0.3.0" {
		t.Errorf("header not recorded: %+v", LoadState())
	}
}

func TestStatus(t *testing.T) {
	at := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	seen := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		version  string
		state    State
		latest   string
		checked  *time.Time
		outdated string
		below    string
	}{
		"github result wins over the server's latest": {"0.1.0", State{LatestVersion: "0.2.0", CheckedAt: &at, ServerLatestVersion: "0.9.0", ServerSeenAt: &seen}, "0.2.0", &at, "true", ""},
		"server latest when GitHub has no result":     {"0.1.0", State{ServerLatestVersion: "0.9.0", ServerSeenAt: &seen}, "0.9.0", &seen, "true", ""},
		"current is newer than latest":                {"0.4.0", State{LatestVersion: "0.3.0", CheckedAt: &at}, "0.3.0", &at, "false", ""},
		"prerelease is older than its release":        {"0.3.0-rc.1", State{LatestVersion: "0.3.0"}, "0.3.0", nil, "true", ""},
		"below the minimum but latest unknown":        {"0.1.0", State{ServerMinVersion: "0.2.0"}, "", nil, "", "true"},
		"at the minimum":                              {"0.2.0", State{ServerMinVersion: "0.2.0"}, "", nil, "", "false"},
		"v prefix in the cache is tolerated":          {"0.1.0", State{LatestVersion: "v0.2.0"}, "0.2.0", nil, "true", ""},
		"unusable cached versions are unknown":        {"0.1.0", State{LatestVersion: "x", ServerMinVersion: "1.x"}, "", nil, "", ""},
		"dev build knows nothing":                     {"dev", State{LatestVersion: "0.3.0", ServerMinVersion: "0.2.0"}, "", nil, "", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Status(tc.version, "abc", tc.state)

			if got.LatestVersion != tc.latest || (tc.checked == nil) != (got.CheckedAt == nil) || (tc.checked != nil && !got.CheckedAt.Equal(*tc.checked)) {
				t.Errorf("latest %q checked %v; want %q %v", got.LatestVersion, got.CheckedAt, tc.latest, tc.checked)
			}
			if flag(got.UpdateAvailable) != tc.outdated || flag(got.BelowMinimum) != tc.below {
				t.Errorf("update_available %q below_minimum %q; want %q %q", flag(got.UpdateAvailable), flag(got.BelowMinimum), tc.outdated, tc.below)
			}
			wantsUpgrade := tc.outdated == "true" || tc.below == "true"
			if (got.UpgradeCommand != "") != wantsUpgrade || (got.URL != "") != (tc.latest != "") {
				t.Errorf("upgrade_command %q url %q", got.UpgradeCommand, got.URL)
			}
		})
	}
}

func flag(b *bool) string {
	switch {
	case b == nil:
		return ""
	case *b:
		return "true"
	}
	return "false"
}
