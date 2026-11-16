package doctor

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/update"
)

func advertising(latest, minimum string, routes map[string]http.HandlerFunc) map[string]http.HandlerFunc {
	wrapped := map[string]http.HandlerFunc{}
	for path, h := range routes {
		wrapped[path] = func(w http.ResponseWriter, r *http.Request) {
			if latest != "" {
				w.Header().Set(update.LatestHeader, latest)
			}
			if minimum != "" {
				w.Header().Set(update.MinHeader, minimum)
			}
			h(w, r)
		}
	}
	return wrapped
}

func asVersion(t *testing.T, version string) {
	t.Helper()
	previous := update.Version
	update.Version = version
	t.Cleanup(func() { update.Version = previous })
}

func versionCheck(t *testing.T, version, latest, minimum string) (Check, *Report) {
	t.Helper()
	asVersion(t, version)
	report := run(t, serve(t, advertising(latest, minimum, healthy())), Options{})
	return check(t, report, "cli_version"), report
}

func TestACurrentCLINamesItsVersion(t *testing.T) {
	got, _ := versionCheck(t, "1.2.3", "1.2.3", "")

	if got.Status != OK || got.Message != "sel 1.2.3 is the latest release" {
		t.Errorf("cli_version = %+v", got)
	}
}

func TestANewerReleaseWarnsWithTheUpgradeCommandAndLeavesTheExitAlone(t *testing.T) {
	got, report := versionCheck(t, "1.2.3", "1.3.0", "")

	if got.Status != Warn || got.Fix != update.UpgradeCommand || got.Detail["latest_version"] != "1.3.0" {
		t.Errorf("cli_version = %+v", got)
	}
	if report.Status != "warn" || report.Err() != nil {
		t.Errorf("status %q, err %v", report.Status, report.Err())
	}
}

func TestAVersionBelowTheServersMinimumWarnsInsteadOfFailing(t *testing.T) {
	got, report := versionCheck(t, "1.2.3", "1.3.0", "1.2.4")

	if got.Status != Warn || got.Detail["below_minimum"] != true || got.Detail["min_version"] != "1.2.4" || got.Fix != update.UpgradeCommand {
		t.Errorf("cli_version = %+v", got)
	}
	if report.Err() != nil {
		t.Errorf("err = %v", report.Err())
	}
}

func TestWithoutVersionInformationTheCheckIsSkippedWithAReason(t *testing.T) {
	for name, version := range map[string]string{"no headers": "1.2.3", "dev build": "dev", "unparseable": "banana"} {
		t.Run(name, func(t *testing.T) {
			latest := ""
			if name != "no headers" {
				latest = "9.9.9"
			}
			got, _ := versionCheck(t, version, latest, "")

			if got.Status != Skip || got.Message == "" {
				t.Errorf("cli_version = %+v", got)
			}
		})
	}
}

func TestOnlyHeadersFromThisRunsOwnCallsCountWhenTheCacheStartedEmpty(t *testing.T) {
	asVersion(t, "1.0.0")
	host := serve(t, advertising("2.0.0", "", healthy()))
	isolate(t, token)
	if st := update.LoadState(); st.ServerLatestVersion != "" {
		t.Fatalf("cache not empty before the run: %+v", st)
	}

	report := Run(Options{Hostname: host, Timeout: time.Minute, Repo: inRepo})

	if got := check(t, report, "cli_version"); got.Status != Warn || got.Detail["latest_version"] != "2.0.0" {
		t.Errorf("cli_version = %+v", got)
	}
}

func TestTheVersionCheckMakesNoRequestOfItsOwn(t *testing.T) {
	count := func(version, latest string) (int64, Check) {
		asVersion(t, version)
		var n atomic.Int64
		routes := advertising(latest, "", healthy())
		for path, h := range routes {
			routes[path] = func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				h(w, r)
			}
		}
		report := run(t, serve(t, routes), Options{})
		return n.Load(), check(t, report, "cli_version")
	}

	withCheck, active := count("1.0.0", "2.0.0")
	withoutCheck, inactive := count("dev", "2.0.0")

	if active.Status != Warn || inactive.Status != Skip {
		t.Fatalf("the check did not toggle: %+v vs %+v", active, inactive)
	}
	if withCheck != withoutCheck || withCheck == 0 {
		t.Errorf("requests with the check %d, without %d, want the same non-zero count", withCheck, withoutCheck)
	}
}

func TestTheVersionCheckIsSkippedWhenTheServerCheckFailed(t *testing.T) {
	asVersion(t, "1.0.0")
	t.Cleanup(api.SetClock(&fakeClock{}))
	dead := httptest.NewServer(http.NotFoundHandler())
	host := dead.URL
	dead.Close()

	report := run(t, host, Options{})

	got := check(t, report, "cli_version")
	if got.Status != Skip || got.Message != "skipped because server failed" {
		t.Errorf("cli_version = %+v", got)
	}
}

func TestTheVersionCheckNeverFailsWhateverTheServerAdvertises(t *testing.T) {
	versions := []string{"dev", "banana", "0.0.1", "1.2.3", "99.0.0", ""}
	advertised := []string{"", "banana", "0.0.1", "1.2.3", "99.0.0"}
	for _, version := range versions {
		for _, latest := range advertised {
			for _, minimum := range advertised {
				asVersion(t, version)
				isolate(t, token)
				update.RecordServerHeaders(http.Header{update.LatestHeader: {latest}, update.MinHeader: {minimum}})
				s := &state{}

				got := s.checkCLIVersion()

				if got.Status == Fail || got.Name != "cli_version" {
					t.Errorf("version %q latest %q min %q: %+v", version, latest, minimum, got)
				}
			}
		}
	}
}
