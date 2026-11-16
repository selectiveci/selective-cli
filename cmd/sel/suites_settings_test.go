package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	otherOrgSuiteID = "44444444-4444-4444-8444-444444444444"
	suiteSettings   = `{"name":"web-rspec","type":"rspec","retries":{"auto_retry_enabled":true,"max_retries":4},"runs":{"stale_run_timeout_minutes":90},"detection":{"enabled":false,"window_days":21,"stability_window_days":9},"selection":{"mode":"shadow","on_default_branch":true,"map_record_interval_hours":12,"ignore_patterns":["vendor","docs"]}}`
)

type showStub struct {
	mu       sync.Mutex
	requests []string
	listing  string
	detail   string
}

func (s *showStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		showRoute := "/api/v1/orgs/acme/suites/"
		switch {
		case r.Method == "GET" && r.URL.RequestURI() == "/api/v1/orgs/acme/suites?state=all":
			listing := activeSuite + `,` + archivedSuite
			if s.listing != "" {
				listing = s.listing
			}
			_, _ = w.Write([]byte(`{"suites":[` + listing + `]}`))
		case r.Method == "GET" && r.URL.Path == showRoute+activeSuiteID && s.detail != "":
			_, _ = w.Write([]byte(s.detail))
		case r.Method == "GET" && r.URL.Path == showRoute+activeSuiteID:
			_, _ = w.Write([]byte(`{"suite":` + activeSuite + `,"settings":` + suiteSettings + `}`))
		case r.Method == "GET" && r.URL.Path == showRoute+archivedSuiteID:
			_, _ = w.Write([]byte(`{"suite":` + archivedSuite + `,"settings":` + suiteSettings + `}`))
		case r.Method == "GET" && r.URL.Path == showRoute+otherOrgSuiteID:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"suite_not_found","message":"No such suite in this organization."}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSuitesShowAndSettingsPrintTheSameDocumentByNameAndById(t *testing.T) {
	list := "GET /api/v1/orgs/acme/suites?state=all"
	cases := []struct{ want, suite, route string }{
		{activeSuite, "WEB-rspec", "GET /api/v1/orgs/acme/suites/" + activeSuiteID},
		{activeSuite, activeSuiteID, "GET /api/v1/orgs/acme/suites/" + activeSuiteID},
		{archivedSuite, "old-minitest", "GET /api/v1/orgs/acme/suites/" + archivedSuiteID},
	}
	for _, verb := range []string{"show", "settings"} {
		for _, tc := range cases {
			t.Run(verb+" "+tc.suite, func(t *testing.T) {
				stub := &showStub{}
				srv := stub.start(t)

				res := runBinary(t, t.TempDir(), srv.URL, "suites", verb, tc.suite, "--org", "acme", "--json")

				want := `{"v":1,"suite":` + tc.want + `,"settings":` + suiteSettings + `}` + "\n"
				if res.exit != 0 || res.stdout != want || !slices.Equal(stub.requests, []string{list, tc.route}) {
					t.Errorf("exit %d stdout %q requests %v, want exit 0, %q and [list, %q]", res.exit, res.stdout, stub.requests, want, tc.route)
				}
			})
		}
	}
}

func TestSuitesShowMapsAnUnknownOrForeignSuiteToSuiteNotFound(t *testing.T) {
	for _, suite := range []string{otherOrgSuiteID, "nope"} {
		srv := (&showStub{}).start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "show", suite, "--org", "acme", "--json")

		assertRefusal(t, res, 3, "suite_not_found")
	}
}

func TestSuitesShowTextModeNamesTheSuiteAndListsEverySetting(t *testing.T) {
	srv := (&showStub{}).start(t)
	var out bytes.Buffer

	err := suitesShow(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out}, "acme", "old-minitest")

	for _, want := range []string{"old-minitest (minitest, archived 2026-11-10T09:00:00Z) " + archivedSuiteID, "  retries.max_retries: 4\n", "  selection.ignore_patterns: [vendor docs]\n", "  detection.enabled: false\n"} {
		if err != nil || !strings.Contains(out.String(), want) {
			t.Errorf("err %v stdout %q, want it to contain %q", err, out.String(), want)
		}
	}
}

func TestSuitesNameSharedByArchivedSuitesAndNoActiveOneIsRefusedNotGuessed(t *testing.T) {
	const twinID = "33333333-3333-4333-8333-333333333333"
	twin := `{"id":"` + twinID + `","name":"old-minitest","type":"minitest","repo":"acme/web","archived_at":"2026-11-01T09:00:00Z","last_run_at":null}`
	listing := archivedSuite + "," + twin
	list := "GET /api/v1/orgs/acme/suites?state=all"
	assertNamesBothIDs := func(t *testing.T, res cliResult) {
		t.Helper()
		assertRefusal(t, res, 5, "usage_error")
		if !strings.Contains(res.stdout, archivedSuiteID) || !strings.Contains(res.stdout, twinID) || !strings.Contains(res.stdout, "pass one of the ids") {
			t.Errorf("stdout %q, want both ids named and the pass-an-id advice", res.stdout)
		}
	}
	t.Run("show", func(t *testing.T) {
		stub := &showStub{listing: listing}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "show", "old-minitest", "--org", "acme", "--json")

		assertNamesBothIDs(t, res)
		if !slices.Equal(stub.requests, []string{list}) {
			t.Errorf("requests %v, want only [%s]", stub.requests, list)
		}
	})
	t.Run("archive", func(t *testing.T) {
		stub := &archiveStub{status: 200, body: archiveOK(archivedSuite, "updated", false), listing: listing}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", "old-minitest", "--org", "acme", "--json")

		assertNamesBothIDs(t, res)
		if !slices.Equal(stub.requests, []string{list}) {
			t.Errorf("requests %v, want only [%s]", stub.requests, list)
		}
	})
}

func TestSuitesShowRefusesAResponseWithoutASuiteObjectAndASettingsObject(t *testing.T) {
	for name, detail := range map[string]string{
		"empty object":      `{}`,
		"null suite":        `{"suite":null,"settings":` + suiteSettings + `}`,
		"settings an array": `{"suite":` + activeSuite + `,"settings":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := &showStub{detail: detail}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "suites", "show", activeSuiteID, "--org", "acme", "--json")

			assertRefusal(t, res, 4, "server_error")
			if strings.Contains(res.stdout, `"v":1,"suite"`) || strings.Contains(res.stdout, `"settings":`) {
				t.Errorf("stdout %q, want no success document", res.stdout)
			}
		})
	}
}
