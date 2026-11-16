package main

import (
	"bytes"
	"encoding/json"
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
	activeSuiteID   = "11111111-1111-4111-8111-111111111111"
	archivedSuiteID = "22222222-2222-4222-8222-222222222222"
	activeSuite     = `{"id":"` + activeSuiteID + `","name":"web-rspec","type":"rspec","repo":"acme/web","archived_at":null,"last_run_at":null}`
	archivedSuite   = `{"id":"` + archivedSuiteID + `","name":"old-minitest","type":"minitest","repo":"acme/web","archived_at":"2026-11-10T09:00:00Z","last_run_at":null}`
)

type archiveStub struct {
	mu       sync.Mutex
	requests []string
	status   int
	body     string
	twin     string
}

func (s *archiveStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/orgs/acme/suites" && r.URL.Query().Get("state") == "all":
			_, _ = w.Write([]byte(`{"suites":[` + activeSuite + `,` + archivedSuite + s.twin + `]}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/orgs/acme/suites":
			_, _ = w.Write([]byte(`{"suites":[` + activeSuite + `]}`))
		case strings.HasSuffix(r.URL.Path, "/archive"):
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func archiveOK(suite, result string, dryRun bool) string {
	dry := "false"
	if dryRun {
		dry = "true"
	}
	return `{"result":"` + result + `","dry_run":` + dry + `,"suite":` + suite + `}`
}

func TestSuitesArchiveByNameLooksUpAllSuitesAndPutsTheArchiveRoute(t *testing.T) {
	archived := strings.Replace(activeSuite, `"archived_at":null`, `"archived_at":"2026-11-15T10:00:00Z"`, 1)
	stub := &archiveStub{status: 200, body: archiveOK(archived, "updated", false)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", "WEB-rspec", "--org", "acme", "--json")

	want := `{"v":1,"suite":` + archived + `,"result":"updated","dry_run":false}` + "\n"
	wantRequests := []string{"GET /api/v1/orgs/acme/suites?state=all", "PUT /api/v1/orgs/acme/suites/" + activeSuiteID + "/archive"}
	if res.exit != 0 || res.stdout != want || !slices.Equal(stub.requests, wantRequests) {
		t.Errorf("exit %d stdout %q requests %v, want exit 0, %q and %v", res.exit, res.stdout, stub.requests, want, wantRequests)
	}
}

func TestSuitesRestoreByNameFindsAnArchivedSuiteAndDeletesTheArchiveRoute(t *testing.T) {
	restored := strings.Replace(archivedSuite, `"archived_at":"2026-11-10T09:00:00Z"`, `"archived_at":null`, 1)
	stub := &archiveStub{status: 200, body: archiveOK(restored, "updated", false)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "restore", "old-minitest", "--org", "acme", "--json")

	want := `{"v":1,"suite":` + restored + `,"result":"updated","dry_run":false}` + "\n"
	wantRequests := []string{"GET /api/v1/orgs/acme/suites?state=all", "DELETE /api/v1/orgs/acme/suites/" + archivedSuiteID + "/archive"}
	if res.exit != 0 || res.stdout != want || !slices.Equal(stub.requests, wantRequests) {
		t.Errorf("exit %d stdout %q requests %v, want exit 0, %q and %v", res.exit, res.stdout, stub.requests, want, wantRequests)
	}
}

func TestSuitesArchiveAndRestoreByNameTargetTheSameNamedSuiteTheVerbWouldChange(t *testing.T) {
	twinID := "33333333-3333-4333-8333-333333333333"
	twin := `,{"id":"` + twinID + `","name":"web-rspec","type":"rspec","repo":"acme/web","archived_at":"2026-11-01T09:00:00Z","last_run_at":null}`
	cases := []struct{ verb, method, id string }{
		{"archive", "PUT", activeSuiteID},
		{"restore", "DELETE", twinID},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			stub := &archiveStub{status: 200, body: archiveOK(activeSuite, "updated", false), twin: twin}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "suites", tc.verb, "web-rspec", "--org", "acme", "--json")

			want := tc.method + " /api/v1/orgs/acme/suites/" + tc.id + "/archive"
			if res.exit != 0 || stub.requests[len(stub.requests)-1] != want {
				t.Errorf("exit %d requests %v, want the last to be %q", res.exit, stub.requests, want)
			}
		})
	}
}

func TestSuitesArchiveByIdSkipsTheLookup(t *testing.T) {
	stub := &archiveStub{status: 200, body: archiveOK(archivedSuite, "unchanged", false)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", archivedSuiteID, "--org", "acme", "--json")

	want := `{"v":1,"suite":` + archivedSuite + `,"result":"unchanged","dry_run":false}` + "\n"
	if res.exit != 0 || res.stdout != want || len(stub.requests) != 1 || !strings.HasPrefix(stub.requests[0], "PUT ") {
		t.Errorf("exit %d stdout %q requests %v, want exit 0, %q and one PUT", res.exit, res.stdout, stub.requests, want)
	}
}

func TestSuitesArchiveDryRunSendsTheFlagAndReportsDryRunTrue(t *testing.T) {
	stub := &archiveStub{status: 200, body: archiveOK(activeSuite, "updated", true)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", activeSuiteID, "--dry-run", "--org", "acme", "--json")

	want := `{"v":1,"suite":` + activeSuite + `,"result":"updated","dry_run":true}` + "\n"
	if res.exit != 0 || res.stdout != want || stub.requests[0] != "PUT /api/v1/orgs/acme/suites/"+activeSuiteID+"/archive?dry_run=true" {
		t.Errorf("exit %d stdout %q requests %v, want exit 0, %q and dry_run=true on the query", res.exit, res.stdout, stub.requests, want)
	}
}

func TestSuitesArchiveMapsEachServerRefusalToItsExit(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		exit   int
		code   string
	}{
		{"forbidden", 403, `{"error":"forbidden","message":"Only an organization owner or admin can archive or restore suites."}`, nil, 6, "forbidden"},
		{"read-only token", 403, `{"error":"insufficient_scope","message":"This token needs the \"write\" scope.","scope":"write"}`, nil, 6, "insufficient_scope"},
		{"no such suite", 404, `{"error":"suite_not_found","message":"No such suite in this organization."}`, nil, 3, "suite_not_found"},
		{"bad token", 401, `{"error":"unauthorized","message":"Invalid token."}`, nil, 4, "unauthorized"},
		{"dry run refused", 422, `{"error":"invalid_request","message":"This route has no dry run, or dry_run is not true or false."}`, []string{"--dry-run"}, 5, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&archiveStub{status: tc.status, body: tc.body}).start(t)
			args := append([]string{"suites", "archive", activeSuiteID, "--org", "acme", "--json"}, tc.args...)

			res := runBinary(t, t.TempDir(), srv.URL, args...)

			assertRefusal(t, res, tc.exit, tc.code)
			if strings.Contains(res.stdout, `"result"`) {
				t.Errorf("stdout carries a write object: %q", res.stdout)
			}
		})
	}
}

func TestSuitesArchiveUnknownNameIsSuiteNotFoundAndSendsNoWrite(t *testing.T) {
	stub := &archiveStub{status: 200, body: archiveOK(activeSuite, "updated", false)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", "nope", "--org", "acme", "--json")

	assertRefusal(t, res, 3, "suite_not_found")
	if len(stub.requests) != 1 {
		t.Errorf("requests = %v, want only the lookup", stub.requests)
	}
}

func TestSuitesArchiveAndRestoreTextModeIsOneLinePerOutcome(t *testing.T) {
	cases := []struct {
		archive, dryRun bool
		result          string
		want            string
	}{
		{true, false, "updated", "Archived suite \"web-rspec\".\n"},
		{false, false, "updated", "Restored suite \"web-rspec\".\n"},
		{true, true, "updated", "Dry run: would archive suite \"web-rspec\".\n"},
		{false, true, "updated", "Dry run: would restore suite \"web-rspec\".\n"},
		{true, false, "unchanged", "Suite \"web-rspec\" is already archived.\n"},
		{false, false, "unchanged", "Suite \"web-rspec\" is already active.\n"},
	}
	for _, tc := range cases {
		stub := &archiveStub{status: 200, body: archiveOK(activeSuite, tc.result, tc.dryRun)}
		srv := stub.start(t)
		var out, errOut bytes.Buffer

		err := suitesSetArchived(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}, "acme", activeSuiteID, tc.archive, tc.dryRun)

		if err != nil || out.String() != tc.want || errOut.Len() != 0 {
			t.Errorf("archive=%v dryRun=%v %s: err %v stdout %q stderr %q, want %q", tc.archive, tc.dryRun, tc.result, err, out.String(), errOut.String(), tc.want)
		}
	}
}

func TestHelpDeclaresSuitesArchiveAndRestoreAsWrites(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path      string `json:"path"`
				ExitCodes []int  `json:"exit_codes"`
				Mutates   bool   `json:"mutates"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, c := range doc.Help.Commands {
		declared[c.Path] = c.Mutates && slices.Equal(c.ExitCodes, []int{0, 3, 4, 5, 6})
	}
	for _, path := range []string{"suites archive", "suites restore"} {
		if !declared[path] {
			t.Errorf("help --json lacks %s with mutates and exit codes 0,3,4,5,6", path)
		}
	}
}
