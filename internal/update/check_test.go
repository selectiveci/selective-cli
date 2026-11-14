package update

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type releaseStub struct {
	redirect http.HandlerFunc
	api      http.HandlerFunc
	hits     atomic.Int32
	arrived  chan struct{}
}

func stubReleases(t *testing.T, redirect, api http.HandlerFunc) *releaseStub {
	t.Helper()
	stub := &releaseStub{redirect: redirect, api: api, arrived: make(chan struct{}, 8)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.hits.Add(1)
		stub.arrived <- struct{}{}
		if r.URL.Path == "/api" {
			stub.api(w, r)
			return
		}
		stub.redirect(w, r)
	}))
	t.Cleanup(srv.Close)
	previousURL, previousAPI, previousTimeout := releasesURL, releasesAPIURL, checkTimeout
	releasesURL, releasesAPIURL = srv.URL+"/latest", srv.URL+"/api"
	t.Cleanup(func() { releasesURL, releasesAPIURL, checkTimeout = previousURL, previousAPI, previousTimeout })
	return stub
}

func tagRedirect(tag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/selectiveci/selective-cli/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	}
}

func apiTag(tag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"tag_name":"` + tag + `"}`)) }
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }
}

func hang(release <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

func checkOnce(t *testing.T) {
	t.Helper()
	checker := StartCheck(Version, func(string) string { return "" })
	<-checker.done
	checker.Stop()
}

func TestCheckReadsTheTagFromTheRedirectWithoutFollowingIt(t *testing.T) {
	isolated(t, "0.1.0")
	stub := stubReleases(t, tagRedirect("v0.3.0"), status(500))

	checkOnce(t)

	st := LoadState()
	if st.LatestVersion != "0.3.0" || st.CheckedAt == nil || st.FailedAt != nil || stub.hits.Load() != 1 {
		t.Errorf("state %+v after %d request(s), want latest 0.3.0 from the redirect alone", st, stub.hits.Load())
	}
}

func TestCheckFallsBackToTheAPI(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"redirect fails":          status(500),
		"redirect is not a tag":   func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", "/login"); w.WriteHeader(302) },
		"redirect tag not semver": tagRedirect("nightly"),
	}
	for name, redirect := range cases {
		t.Run(name, func(t *testing.T) {
			isolated(t, "0.1.0")
			stubReleases(t, redirect, apiTag("v0.4.0"))

			checkOnce(t)

			if st := LoadState(); st.LatestVersion != "0.4.0" || st.FailedAt != nil {
				t.Errorf("state = %+v, want latest 0.4.0 from the API", st)
			}
		})
	}
}

func TestFailedCheckBacksOffAndKeepsTheOldResult(t *testing.T) {
	isolated(t, "0.1.0")
	stale := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	if err := saveState(State{LatestVersion: "0.2.0", CheckedAt: &stale}); err != nil {
		t.Fatal(err)
	}
	stub := stubReleases(t, status(500), status(500))

	checkOnce(t)
	first := stub.hits.Load()
	checkOnce(t)

	st := LoadState()
	if st.FailedAt == nil || st.LatestVersion != "0.2.0" || !st.CheckedAt.Equal(stale) {
		t.Errorf("state = %+v, want failed_at set and the old result kept", st)
	}
	if stub.hits.Load() != first {
		t.Errorf("requests grew from %d to %d during the backoff", first, stub.hits.Load())
	}
}

func TestSlowFetchHitsTheTimeoutAndBacksOff(t *testing.T) {
	isolated(t, "0.1.0")
	release := make(chan struct{})
	defer close(release)
	stubReleases(t, hang(release), hang(release))
	checkTimeout = 150 * time.Millisecond

	started := time.Now()
	checkOnce(t)

	if st := LoadState(); st.FailedAt == nil || st.LatestVersion != "" {
		t.Errorf("state = %+v, want a recorded failure after the timeout", st)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("took %v with a 150ms timeout", elapsed)
	}
}

func TestStopCancelsTheFetchWithoutRecordingAFailure(t *testing.T) {
	dir := isolated(t, "0.1.0")
	release := make(chan struct{})
	defer close(release)
	stub := stubReleases(t, hang(release), hang(release))
	checker := StartCheck(Version, func(string) string { return "" })
	select {
	case <-stub.arrived:
	case <-time.After(2 * time.Second):
		t.Fatal("the check never reached the stub, so the cancel path was not exercised")
	}

	started := time.Now()
	checker.Stop()

	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("Stop took %v while the fetch was hanging, want under 500ms", elapsed)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); !os.IsNotExist(err) {
		t.Errorf("a cancelled fetch wrote the cache (stat err = %v), which would back off the next command", err)
	}
}

func TestDue(t *testing.T) {
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := at.Add(-d); return &v }
	cases := map[string]struct {
		version string
		state   State
		want    bool
	}{
		"never checked":              {"0.1.0", State{}, true},
		"checked an hour ago":        {"0.1.0", State{CheckedAt: ago(time.Hour)}, false},
		"checked 25 hours ago":       {"0.1.0", State{CheckedAt: ago(25 * time.Hour)}, true},
		"failed 10 minutes ago":      {"0.1.0", State{FailedAt: ago(10 * time.Minute)}, false},
		"failed 2 hours ago":         {"0.1.0", State{FailedAt: ago(2 * time.Hour)}, true},
		"dev build":                  {"dev", State{}, false},
		"only the server has spoken": {"0.1.0", State{ServerLatestVersion: "0.3.0", ServerSeenAt: ago(time.Minute)}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := due(tc.state, tc.version, at); got != tc.want {
				t.Errorf("due = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnabled(t *testing.T) {
	for _, name := range append([]string{OptOutVariable}, ciVariables...) {
		t.Run(name, func(t *testing.T) {
			if Enabled(func(key string) string { return map[string]string{name: "1"}[key] }) {
				t.Errorf("Enabled with %s set", name)
			}
		})
	}
	if !Enabled(func(string) string { return "" }) {
		t.Error("not Enabled in a clean environment")
	}
}

func TestNotice(t *testing.T) {
	yes, no := true, false
	url := ReleaseURL("0.3.0")
	cases := map[string]struct {
		info Info
		want string
	}{
		"outdated": {
			Info{Version: "0.1.0", UpdateAvailable: &yes, LatestVersion: "0.3.0", URL: url},
			"\nA new release of sel is available: 0.1.0 → 0.3.0\nTo upgrade, run: " + UpgradeCommand + "\n" + url + "\n",
		},
		"below the server's minimum": {
			Info{Version: "0.1.0", UpdateAvailable: &yes, LatestVersion: "0.3.0", BelowMinimum: &yes, MinVersion: "0.2.0", URL: url},
			"\nsel 0.1.0 is no longer supported: this server needs 0.2.0 or later\nTo upgrade, run: " + UpgradeCommand + "\n" + url + "\n",
		},
		"below the minimum with no latest known": {
			Info{Version: "0.1.0", BelowMinimum: &yes, MinVersion: "0.2.0"},
			"\nsel 0.1.0 is no longer supported: this server needs 0.2.0 or later\nTo upgrade, run: " + UpgradeCommand + "\n" + releasesPage + "\n",
		},
		"up to date":    {Info{Version: "0.3.0", UpdateAvailable: &no, BelowMinimum: &no}, ""},
		"nothing known": {Info{Version: "dev"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Notice(tc.info); got != tc.want {
				t.Errorf("Notice = %q, want %q", got, tc.want)
			}
			if strings.Contains(tc.want, "\x1b") {
				t.Error("notice has escape codes")
			}
		})
	}
}

func TestConcurrentWritersDoNotLoseEachOthersFields(t *testing.T) {
	isolated(t, "0.1.0")
	for round := 0; round < 100; round++ {
		if err := os.Remove(filepath.Join(os.Getenv("SELECTIVE_CONFIG_DIR"), cacheFile)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan struct{}, 2)
		go func() {
			<-start
			RecordServerHeaders(headers("0.3.0", ""))
			done <- struct{}{}
		}()
		go func() {
			<-start
			modifyState(func(st *State) { st.LatestVersion = "0.2.0" })
			done <- struct{}{}
		}()
		close(start)
		<-done
		<-done

		if st := LoadState(); st.ServerLatestVersion != "0.3.0" || st.LatestVersion != "0.2.0" {
			t.Fatalf("round %d lost an update: %+v", round, st)
		}
	}
}
