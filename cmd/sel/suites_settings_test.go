package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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

const settingsRoute = "/api/v1/orgs/acme/suites/" + activeSuiteID + "/settings"

type settingsStub struct {
	mu       sync.Mutex
	requests []string
	bodies   []string
	headers  []http.Header
	status   int
	reply    string
}

func (s *settingsStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.bodies = append(s.bodies, string(body))
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.RequestURI() == "/api/v1/orgs/acme/suites?state=all":
			_, _ = w.Write([]byte(`{"suites":[` + activeSuite + `]}`))
		case r.Method == "PUT" && r.URL.Path == settingsRoute:
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.reply))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *settingsStub) requireRequests(t *testing.T, n int) {
	t.Helper()
	if len(s.requests) < n || len(s.bodies) < n || len(s.headers) < n {
		t.Fatalf("recorded %d requests %v, want at least %d", len(s.requests), s.requests, n)
	}
}

func (s *settingsStub) puts() int {
	n := 0
	for _, r := range s.requests {
		if strings.HasPrefix(r, "PUT ") {
			n++
		}
	}
	return n
}

func settingsReply(result string, dryRun bool) string {
	return `{"suite":` + activeSuite + `,"settings":` + suiteSettings + `,"result":"` + result + `","dry_run":` + strconv.FormatBool(dryRun) + `}`
}

func TestSuitesSettingsSetSendsOnlyTheNamedFieldsWithTheirJSONTypesAndNoIdempotencyKey(t *testing.T) {
	cases := []struct {
		name, body string
		args       []string
	}{
		{"every key", `{"detection":{"enabled":false,"stability_window_days":9,"window_days":21},"name":"renamed","retries":{"auto_retry_enabled":true,"max_retries":0},"runs":{"stale_run_timeout_minutes":60},"selection":{"ignore_patterns":["vendor","docs"],"map_record_interval_hours":6,"mode":"live","on_default_branch":true},"type":"minitest"}`, []string{
			"--set", "name=renamed", "--set", "type=minitest",
			"--set", "retries.auto_retry_enabled=true", "--set", "retries.max_retries=0",
			"--set", "runs.stale_run_timeout_minutes=60",
			"--set", "detection.enabled=false", "--set", "detection.window_days=21", "--set", "detection.stability_window_days=9",
			"--set", "selection.mode=live", "--set", "selection.on_default_branch=true", "--set", "selection.map_record_interval_hours=6", "--set", "selection.ignore_patterns=vendor,docs",
		}},
		{"one field", `{"selection":{"mode":"live"}}`, []string{"--set", "selection.mode=live"}},
		{"empty patterns clear the list", `{"selection":{"ignore_patterns":[]}}`, []string{"--set", "selection.ignore_patterns="}},
		{"patterns are trimmed around each element", `{"selection":{"ignore_patterns":["a","b","c d"]}}`, []string{"--set", "selection.ignore_patterns= a, b ,c d"}},
		{"a negative count still reaches the server", `{"retries":{"max_retries":-1}}`, []string{"--set", "retries.max_retries=-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &settingsStub{status: 200, reply: settingsReply("updated", false)}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append([]string{"suites", "settings", "web-rspec", "--org", "acme", "--json"}, tc.args...)...)

			stub.requireRequests(t, 2)
			want := `{"v":1,"suite":` + activeSuite + `,"settings":` + suiteSettings + `,"result":"updated","dry_run":false}` + "\n"
			wantRequests := []string{"GET /api/v1/orgs/acme/suites?state=all", "PUT " + settingsRoute}
			if res.exit != 0 || res.stdout != want || !slices.Equal(stub.requests, wantRequests) || stub.bodies[1] != tc.body {
				t.Errorf("exit %d stdout %q requests %v bodies %q, want exit 0, %q, %v and body %s", res.exit, res.stdout, stub.requests, stub.bodies, want, wantRequests, tc.body)
			}
			if key := stub.headers[1].Get("Idempotency-Key"); key != "" {
				t.Errorf("Idempotency-Key = %q, want none", key)
			}
		})
	}
}

func TestSuitesSettingsDryRunSendsTheQueryAndRepeatsPassUnchangedThrough(t *testing.T) {
	cases := []struct {
		result string
		dryRun bool
		args   []string
	}{
		{"updated", true, []string{"--dry-run"}},
		{"unchanged", false, nil},
	}
	for _, tc := range cases {
		stub := &settingsStub{status: 200, reply: settingsReply(tc.result, tc.dryRun)}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, append([]string{"suites", "settings", "web-rspec", "--org", "acme", "--json", "--set", "selection.mode=shadow"}, tc.args...)...)

		stub.requireRequests(t, 2)
		wantPut := "PUT " + settingsRoute
		if tc.dryRun {
			wantPut += "?dry_run=true"
		}
		if res.exit != 0 || stub.requests[1] != wantPut || !strings.Contains(res.stdout, `"result":"`+tc.result+`","dry_run":`+strconv.FormatBool(tc.dryRun)+`}`) {
			t.Errorf("exit %d stdout %q requests %v, want exit 0, result %s and %s", res.exit, res.stdout, stub.requests, tc.result, wantPut)
		}
	}
}

func TestSuitesSettingsRefusesBadInputBeforeSendingAnything(t *testing.T) {
	cases := map[string][]string{
		"unknown key":                  {"--set", "nosuch.key=1"},
		"unknown field":                {"--set", "retries.nosuch=1"},
		"nested suite key":             {"--set", "suite.name=x"},
		"no equals sign":               {"--set", "selection.mode"},
		"bad flag value":               {"--set", "retries.auto_retry_enabled=yes"},
		"bad count":                    {"--set", "retries.max_retries=many"},
		"unknown mode":                 {"--set", "selection.mode=bogus"},
		"one bad among good":           {"--set", "selection.mode=live", "--set", "detection.window_days=1.5"},
		"dry run without --set":        {"--dry-run"},
		"empty pattern between commas": {"--set", "selection.ignore_patterns=a,,b"},
		"blank pattern between commas": {"--set", "selection.ignore_patterns=a, ,b"},
		"trailing comma":               {"--set", "selection.ignore_patterns=a,"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &settingsStub{status: 200, reply: settingsReply("updated", false)}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append([]string{"suites", "settings", "web-rspec", "--org", "acme", "--json"}, args...)...)

			assertRefusal(t, res, 5, "usage_error")
			if len(stub.requests) != 0 {
				t.Errorf("requests = %v, want none", stub.requests)
			}
		})
	}
}

func TestSuitesSettingsRefusesAPutResponseWithoutASuiteObjectAndASettingsObject(t *testing.T) {
	for name, reply := range map[string]string{
		"empty object":      `{"result":"updated","dry_run":false}`,
		"null suite":        `{"suite":null,"settings":` + suiteSettings + `,"result":"updated","dry_run":false}`,
		"settings an array": `{"suite":` + activeSuite + `,"settings":[],"result":"updated","dry_run":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := &settingsStub{status: 200, reply: reply}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "suites", "settings", "web-rspec", "--org", "acme", "--json", "--set", "selection.mode=live")

			assertRefusal(t, res, 4, "server_error")
			wantRequests := []string{"GET /api/v1/orgs/acme/suites?state=all", "PUT " + settingsRoute}
			if strings.Contains(res.stdout, `"result"`) || !slices.Equal(stub.requests, wantRequests) {
				t.Errorf("stdout %q requests %v, want no success document and %v", res.stdout, stub.requests, wantRequests)
			}
		})
	}
}

func TestSuitesSettingsRefusesAPutResponseWithAMissingOrUnknownResult(t *testing.T) {
	suiteAndSettings := `"suite":` + activeSuite + `,"settings":` + suiteSettings
	for name, reply := range map[string]string{
		"missing":        `{` + suiteAndSettings + `,"dry_run":false}`,
		"empty":          `{` + suiteAndSettings + `,"result":"","dry_run":false}`,
		"null":           `{` + suiteAndSettings + `,"result":null,"dry_run":false}`,
		"unknown":        `{` + suiteAndSettings + `,"result":"deleted","dry_run":false}`,
		"wrong type":     `{` + suiteAndSettings + `,"result":1,"dry_run":false}`,
		"different case": `{` + suiteAndSettings + `,"result":"Updated","dry_run":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub := &settingsStub{status: 200, reply: reply}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "suites", "settings", "web-rspec", "--org", "acme", "--set", "selection.mode=live")

			assertRefusal(t, res, 4, "server_error")
			wantRequests := []string{"GET /api/v1/orgs/acme/suites?state=all", "PUT " + settingsRoute}
			if strings.Contains(res.stdout, "Changed settings") || !slices.Equal(stub.requests, wantRequests) {
				t.Errorf("stdout %q requests %v, want no success sentence and %v", res.stdout, stub.requests, wantRequests)
			}
		})
	}
}

func TestSuitesSettingsTextModeNamesTheSuiteFromTheResponseAfterARename(t *testing.T) {
	renamed := strings.Replace(activeSuite, `"name":"web-rspec"`, `"name":"renamed"`, 1)
	cases := []struct {
		name, suite, want string
	}{
		{"returned name wins", renamed, "Changed settings of suite \"renamed\".\n"},
		{"argument when the suite has no name", strings.Replace(activeSuite, `"name":"web-rspec",`, "", 1), "Changed settings of suite \"web-rspec\".\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := `{"suite":` + tc.suite + `,"settings":` + suiteSettings + `,"result":"updated","dry_run":false}`
			stub := &settingsStub{status: 200, reply: reply}
			srv := stub.start(t)
			var out bytes.Buffer

			err := suitesChangeSettings(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out}, "acme", "web-rspec", map[string]any{"name": "renamed"}, false)

			wantRequests := []string{"GET /api/v1/orgs/acme/suites?state=all", "PUT " + settingsRoute}
			if err != nil || out.String() != tc.want || !slices.Equal(stub.requests, wantRequests) {
				t.Errorf("err %v stdout %q requests %v, want nil, %q and %v", err, out.String(), stub.requests, tc.want, wantRequests)
			}
		})
	}
}

func TestSuitesSettingsMapsServerRefusalsAndNeverRetriesTheKeylessPut(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reply  string
		exit   int
		want   string
	}{
		{"validation fields pass through", 422, `{"error":"validation_failed","message":"Invalid settings.","fields":{"retries.max_retries":["must be greater than or equal to 0"]}}`, 5, `"fields":{"retries.max_retries":["must be greater than or equal to 0"]}`},
		{"read-only token", 403, `{"error":"insufficient_scope","message":"This token needs the \"write\" scope.","scope":"write"}`, 6, `"error":"insufficient_scope"`},
		{"transient failure", 503, `{"error":"server_error","message":"try later"}`, 4, `"error":"server_error"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &settingsStub{status: tc.status, reply: tc.reply}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "suites", "settings", "web-rspec", "--org", "acme", "--json", "--set", "retries.max_retries=-1")

			if res.exit != tc.exit || !strings.Contains(res.stdout, tc.want) || stub.puts() != 1 {
				t.Errorf("exit %d stdout %q PUTs %d, want exit %d, %s and one PUT", res.exit, res.stdout, stub.puts(), tc.exit, tc.want)
			}
		})
	}
}

func TestSuitesSettingsValidatesTheModeAgainstSetupSelectionModes(t *testing.T) {
	known := setup.SelectionModes
	setup.SelectionModes = append(slices.Clone(known), "turbo")
	t.Cleanup(func() { setup.SelectionModes = known })
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	cases := []struct {
		mode string
		exit int
		puts int
	}{
		{"turbo", 0, 1},
		{"shadow", 0, 1},
		{"bogus", 5, 0},
	}
	for _, tc := range cases {
		stub := &settingsStub{status: 200, reply: settingsReply("updated", false)}
		srv := stub.start(t)
		t.Setenv("SELECTIVE_HOST", srv.URL)
		var out, errOut bytes.Buffer

		_, code := runCommand(newRoot(), []string{"suites", "settings", "web-rspec", "--org", "acme", "--json", "--set", "selection.mode=" + tc.mode}, &out, &errOut)

		if code != tc.exit || stub.puts() != tc.puts {
			t.Errorf("mode %s: exit %d with %d PUTs (%v), want exit %d and %d", tc.mode, code, stub.puts(), stub.bodies, tc.exit, tc.puts)
		}
	}
}

func TestSuitesSettingsTextModeIsOneLinePerOutcome(t *testing.T) {
	cases := []struct {
		result string
		dryRun bool
		want   string
	}{
		{"updated", false, "Changed settings of suite \"web-rspec\".\n"},
		{"updated", true, "Dry run: would change settings of suite \"web-rspec\".\n"},
		{"unchanged", false, "Settings of suite \"web-rspec\" already match.\n"},
	}
	for _, tc := range cases {
		srv := (&settingsStub{status: 200, reply: settingsReply(tc.result, tc.dryRun)}).start(t)
		var out bytes.Buffer

		err := suitesChangeSettings(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out}, "acme", "web-rspec", map[string]any{"name": "x"}, tc.dryRun)

		if err != nil || out.String() != tc.want {
			t.Errorf("%s dryRun=%v: err %v stdout %q, want %q", tc.result, tc.dryRun, err, out.String(), tc.want)
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
