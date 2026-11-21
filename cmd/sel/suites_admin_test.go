package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	listing  string
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
			listing := activeSuite + `,` + archivedSuite + s.twin
			if s.listing != "" {
				listing = s.listing
			}
			_, _ = w.Write([]byte(`{"suites":[` + listing + `]}`))
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

func TestSuitesArchiveByNamePicksTheActiveTwinWhenTheArchivedOneIsListedFirst(t *testing.T) {
	twinID := "33333333-3333-4333-8333-333333333333"
	twin := `{"id":"` + twinID + `","name":"web-rspec","type":"rspec","repo":"acme/web","archived_at":"2026-11-01T09:00:00Z","last_run_at":null}`
	stub := &archiveStub{status: 200, body: archiveOK(activeSuite, "updated", false), listing: twin + `,` + activeSuite + `,` + archivedSuite}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", "web-rspec", "--org", "acme", "--json")

	want := "PUT /api/v1/orgs/acme/suites/" + activeSuiteID + "/archive"
	if res.exit != 0 || stub.requests[len(stub.requests)-1] != want {
		t.Errorf("exit %d requests %v, want the last to be %q", res.exit, stub.requests, want)
	}
}

func TestSuitesArchiveByIdListsOnceThenSendsThatId(t *testing.T) {
	stub := &archiveStub{status: 200, body: archiveOK(archivedSuite, "unchanged", false)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", archivedSuiteID, "--org", "acme", "--json")

	want := `{"v":1,"suite":` + archivedSuite + `,"result":"unchanged","dry_run":false}` + "\n"
	if res.exit != 0 || res.stdout != want || len(stub.requests) != 2 || !strings.HasPrefix(stub.requests[1], "PUT ") || !strings.Contains(stub.requests[1], archivedSuiteID) {
		t.Errorf("exit %d stdout %q requests %v, want exit 0, %q, the lookup and one PUT of the id", res.exit, res.stdout, stub.requests, want)
	}
}

func TestSuitesArchiveDryRunSendsTheFlagAndReportsDryRunTrue(t *testing.T) {
	stub := &archiveStub{status: 200, body: archiveOK(activeSuite, "updated", true)}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "archive", activeSuiteID, "--dry-run", "--org", "acme", "--json")

	want := `{"v":1,"suite":` + activeSuite + `,"result":"updated","dry_run":true}` + "\n"
	if res.exit != 0 || res.stdout != want || stub.requests[len(stub.requests)-1] != "PUT /api/v1/orgs/acme/suites/"+activeSuiteID+"/archive?dry_run=true" {
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

type deleteCall struct {
	method, path, body string
	header             http.Header
}

type deleteStub struct {
	mu        sync.Mutex
	writes    []deleteCall
	polls     []string
	lookups   int
	orgGets   int
	replies   []deleteReply
	approvals []string
	listing   string
	route     string
	stall     time.Duration
}

type deleteReply struct {
	status int
	body   string
}

func (s *deleteStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.stall > 0 {
			select {
			case <-time.After(s.stall):
			case <-r.Context().Done():
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/orgs":
			s.orgGets++
			_, _ = w.Write([]byte(`{"organizations":[{"slug":"acme","name":"Acme","role":"owner","github_account":"acme"}]}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/orgs/acme/suites":
			s.lookups++
			listing := activeSuite + `,` + archivedSuite
			if s.listing != "" {
				listing = s.listing
			}
			_, _ = w.Write([]byte(`{"suites":[` + listing + `]}`))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/approvals/"):
			s.polls = append(s.polls, r.URL.Path)
			_, _ = w.Write([]byte(`{"approval":` + s.approvals[min(len(s.polls)-1, len(s.approvals)-1)] + `}`))
		case r.Method == "DELETE" && r.URL.Path == cmp.Or(s.route, deleteRoute):
			body, _ := io.ReadAll(r.Body)
			s.writes = append(s.writes, deleteCall{r.Method, r.URL.RequestURI(), string(body), r.Header.Clone()})
			reply := s.replies[min(len(s.writes)-1, len(s.replies)-1)]
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *deleteStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups + len(s.polls) + len(s.writes)
}

const deleteRoute = "/api/v1/orgs/acme/suites/" + archivedSuiteID

var (
	deletePending = deleteReply{202, `{"result":"pending_approval","dry_run":false,"approval":` + pendingApproval.json() + `}`}
	deleteDone    = deleteReply{200, archiveOK(archivedSuite, "deleted", false)}
)

func TestSuitesDeleteWithoutWaitExitsSevenWithThePendingObjectAfterOneCall(t *testing.T) {
	stub := &deleteStub{replies: []deleteReply{deletePending}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--org", "acme", "--json")

	want := `{"v":1,"result":"pending_approval","dry_run":false,"approval":` + pendingApproval.json() + `}` + "\n"
	if res.exit != 7 || res.stdout != want || len(stub.writes) != 1 || stub.writes[0].path != deleteRoute {
		t.Errorf("exit %d stdout %q writes %+v, want exit 7, %q and one DELETE of the suite", res.exit, res.stdout, stub.writes, want)
	}
}

func TestSuitesDeleteWaitResendsTheIdenticalKeylessRequestOnceApproved(t *testing.T) {
	approved := approvalFixture{status: "approved", expiresAt: "2026-11-15T10:00:00.123456Z", decidedAt: "2026-11-14T10:05:00Z"}
	stub := &deleteStub{replies: []deleteReply{deletePending, deleteDone}, approvals: []string{pendingApproval.json(), approved.json()}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--wait=2m", "--org", "acme", "--json")

	lines := strings.Split(strings.TrimSuffix(res.stdout, "\n"), "\n")
	last := `{"v":1,"event":"done","result":"deleted","suite":` + archivedSuite + `}`
	if res.exit != 0 || len(lines) < 3 || !strings.HasPrefix(lines[0], `{"v":1,"result":"pending_approval"`) || !strings.Contains(lines[1], `"event":"waiting"`) || lines[len(lines)-1] != last {
		t.Fatalf("exit %d stdout %q, want exit 0 with pending, waiting and %q", res.exit, res.stdout, last)
	}
	if len(stub.writes) != 2 || !reflect.DeepEqual(stub.writes[0], stub.writes[1]) {
		t.Fatalf("writes = %+v, want two identical requests", stub.writes)
	}
	for _, w := range stub.writes {
		if _, sent := w.header["Idempotency-Key"]; sent || w.method != "DELETE" || w.path != deleteRoute || w.body != "" {
			t.Errorf("write = %+v, want an empty-bodied DELETE of %s with no Idempotency-Key", w, deleteRoute)
		}
	}
	if stub.lookups != 1 {
		t.Errorf("suite list requests = %d, want 1 across pending, approval and re-send", stub.lookups)
	}
}

func TestSuitesDeleteByNameLooksUpTheArchivedSuiteAndDeletesItById(t *testing.T) {
	stub := &deleteStub{replies: []deleteReply{deleteDone}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", "old-minitest", "--yes", "--org", "acme", "--json")

	want := `{"v":1,"suite":` + archivedSuite + `,"result":"deleted","dry_run":false}` + "\n"
	if res.exit != 0 || res.stdout != want || stub.lookups != 1 || len(stub.writes) != 1 || stub.writes[0].path != deleteRoute {
		t.Errorf("exit %d stdout %q lookups %d writes %+v, want exit 0, %q and a DELETE of %s", res.exit, res.stdout, stub.lookups, stub.writes, want, archivedSuiteID)
	}
}

func TestSuitesDeleteMapsEachServerRefusalToItsExit(t *testing.T) {
	cases := []struct {
		name  string
		reply deleteReply
		exit  int
		code  string
	}{
		{"not archived", deleteReply{409, `{"error":"suite_not_archived","message":"Archive the suite first."}`}, 5, "suite_not_archived"},
		{"has runs", deleteReply{409, `{"error":"suite_has_runs","message":"The suite still has runs."}`}, 5, "suite_has_runs"},
		{"no such suite", deleteReply{404, `{"error":"suite_not_found","message":"No such suite in this organization."}`}, 3, "suite_not_found"},
		{"approval denied", deleteReply{403, `{"error":"approval_denied","message":"A person denied this action."}`}, 6, "approval_denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&deleteStub{replies: []deleteReply{tc.reply}}).start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--org", "acme", "--json")

			assertRefusal(t, res, tc.exit, tc.code)
		})
	}
}

func TestSuitesDeleteWithoutYesIsAUsageErrorBeforeAnyRequest(t *testing.T) {
	stub := &deleteStub{replies: []deleteReply{deleteDone}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", "old-minitest", "--org", "acme", "--json")

	assertRefusal(t, res, 5, "usage_error")
	if n := stub.requestCount(); n != 0 {
		t.Errorf("made %d requests, want none", n)
	}
}

func TestSuitesDeleteDryRunNeedsNoYesAndAGatedOneDoesNotPoll(t *testing.T) {
	gatedDry := deleteReply{200, `{"result":"pending_approval","dry_run":true}`}
	stub := &deleteStub{replies: []deleteReply{gatedDry}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--dry-run", "--wait", "--org", "acme", "--json")

	if res.exit != 7 || !strings.Contains(res.stdout, `"dry_run":true`) || strings.Contains(res.stdout, `"approval"`) || len(stub.polls) != 0 || stub.writes[0].path != deleteRoute+"?dry_run=true" {
		t.Errorf("exit %d stdout %q polls %v writes %+v, want exit 7, dry_run true, no approval, zero polls", res.exit, res.stdout, stub.polls, stub.writes)
	}
}

func TestSuitesDeleteWaitByAUserTokenReportsDeletedThenDoneWithoutPolling(t *testing.T) {
	stub := &deleteStub{replies: []deleteReply{deleteDone}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--wait", "--org", "acme", "--json")

	want := `{"v":1,"suite":` + archivedSuite + `,"result":"deleted","dry_run":false}` + "\n" +
		`{"v":1,"event":"done","result":"deleted","suite":` + archivedSuite + `}` + "\n"
	if res.exit != 0 || res.stdout != want || len(stub.polls) != 0 || len(stub.writes) != 1 {
		t.Errorf("exit %d stdout %q polls %v writes %d, want exit 0, %q, no polls and one write", res.exit, res.stdout, stub.polls, len(stub.writes), want)
	}
}

func TestSuitesDeleteDeclaresItsGatedWriteContractInHelp(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path        string   `json:"path"`
				ExitCodes   []int    `json:"exit_codes"`
				Events      []string `json:"events"`
				WaitDefault string   `json:"wait_default"`
				Mutates     bool     `json:"mutates"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Help.Commands {
		if c.Path != "suites delete" {
			continue
		}
		if !slices.Equal(c.ExitCodes, []int{0, 3, 4, 5, 6, 7, 8}) || !slices.Equal(c.Events, []string{"waiting", "done"}) || c.WaitDefault != "10m" || !c.Mutates {
			t.Errorf("suites delete = %+v, want exit codes 0,3,4,5,6,7,8, events waiting,done, wait_default 10m and mutates", c)
		}
		return
	}
	t.Error("help --json lists no suites delete")
}

func TestSuitesDeleteTimeoutBoundsTheNameLookupAndExitsEightWithoutDeleting(t *testing.T) {
	stub := &deleteStub{replies: []deleteReply{deleteDone}, stall: 5 * time.Second}
	srv := stub.start(t)

	began := time.Now()
	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", "old-minitest", "--yes", "--timeout=1s", "--org", "acme", "--json")

	assertRefusal(t, res, 8, "timeout")
	if took := time.Since(began); took > 3*time.Second || len(stub.writes) != 0 {
		t.Errorf("took %s with %d writes, want the lookup cut off within 3s and no DELETE", took, len(stub.writes))
	}
}

func TestSuitesDeleteAndRestoreRefuseANameSharedBySeveralSuitesInTheWantedState(t *testing.T) {
	const twinID = "33333333-3333-4333-8333-333333333333"
	twin := `{"id":"` + twinID + `","name":"old-minitest","type":"minitest","repo":"acme/web","archived_at":"2026-11-01T09:00:00Z","last_run_at":null}`
	listing := archivedSuite + "," + twin
	t.Run("delete", func(t *testing.T) {
		stub := &deleteStub{replies: []deleteReply{deleteDone}, listing: listing}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", "old-minitest", "--yes", "--org", "acme", "--json")

		assertRefusal(t, res, 5, "usage_error")
		if !strings.Contains(res.stdout, archivedSuiteID) || !strings.Contains(res.stdout, twinID) || len(stub.writes) != 0 {
			t.Errorf("stdout %q writes %d, want both ids named and no DELETE", res.stdout, len(stub.writes))
		}
	})
	t.Run("restore", func(t *testing.T) {
		stub := &archiveStub{status: 200, body: archiveOK(archivedSuite, "updated", false), listing: listing}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "restore", "old-minitest", "--org", "acme", "--json")

		assertRefusal(t, res, 5, "usage_error")
		if !strings.Contains(res.stdout, archivedSuiteID) || !strings.Contains(res.stdout, twinID) || len(stub.requests) != 1 {
			t.Errorf("stdout %q requests %v, want both ids named and only the lookup", res.stdout, stub.requests)
		}
	})
}

func TestSuitesDeleteTimeoutBoundsTheOrganizationLookupAndExitsEightWithoutDeleting(t *testing.T) {
	t.Setenv("SELECTIVE_ORG", "")
	stub := &deleteStub{replies: []deleteReply{deleteDone}, stall: 5 * time.Second}
	srv := stub.start(t)

	began := time.Now()
	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--timeout=1s", "--json")

	assertRefusal(t, res, 8, "timeout")
	if took := time.Since(began); took > 3*time.Second || len(stub.writes) != 0 {
		t.Errorf("took %s with %d writes, want the organization lookup cut off within 3s and no DELETE", took, len(stub.writes))
	}
}

func TestSuitesDeleteAndRestoreResolveAUuidShapedNameBeforeTreatingItAsAnId(t *testing.T) {
	const (
		shapedName = "44444444-4444-4444-8444-444444444444"
		shapedID   = "55555555-5555-4555-8555-555555555555"
	)
	shaped := `{"id":"` + shapedID + `","name":"` + shapedName + `","type":"rspec","repo":"acme/web","archived_at":"2026-11-01T09:00:00Z","last_run_at":null}`
	t.Run("delete", func(t *testing.T) {
		stub := &deleteStub{replies: []deleteReply{deleteDone}, listing: archivedSuite + "," + shaped, route: "/api/v1/orgs/acme/suites/" + shapedID}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", shapedName, "--yes", "--org", "acme", "--json")

		if want := "/api/v1/orgs/acme/suites/" + shapedID; res.exit != 0 || len(stub.writes) != 1 || stub.writes[0].path != want {
			t.Errorf("exit %d writes %+v, want one DELETE of %s", res.exit, stub.writes, want)
		}
	})
	t.Run("restore", func(t *testing.T) {
		stub := &archiveStub{status: 200, body: archiveOK(shaped, "updated", false), listing: archivedSuite + "," + shaped}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "suites", "restore", shapedName, "--org", "acme", "--json")

		if want := "DELETE /api/v1/orgs/acme/suites/" + shapedID + "/archive"; res.exit != 0 || stub.requests[len(stub.requests)-1] != want {
			t.Errorf("exit %d requests %v, want the last to be %q", res.exit, stub.requests, want)
		}
	})
}

func TestSuitesDeleteSendsAnUnlistedUuidAsTheIdAndLetsTheServerAnswer(t *testing.T) {
	const unlisted = "66666666-6666-4666-8666-666666666666"
	stub := &deleteStub{replies: []deleteReply{{404, `{"error":"suite_not_found","message":"No such suite in this organization."}`}}, route: "/api/v1/orgs/acme/suites/" + unlisted}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", unlisted, "--yes", "--org", "acme", "--json")

	assertRefusal(t, res, 3, "suite_not_found")
	if len(stub.writes) != 1 || stub.writes[0].path != "/api/v1/orgs/acme/suites/"+unlisted {
		t.Errorf("writes = %+v, want one DELETE of %s", stub.writes, unlisted)
	}
}

func TestSuitesDeleteWaitAsksForTheOrganizationOnceAcrossApprovalAndResend(t *testing.T) {
	t.Setenv("SELECTIVE_ORG", "")
	approved := approvalFixture{status: "approved", expiresAt: "2026-11-15T10:00:00.123456Z", decidedAt: "2026-11-14T10:05:00Z"}
	stub := &deleteStub{replies: []deleteReply{deletePending, deleteDone}, approvals: []string{pendingApproval.json(), approved.json()}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--wait=2m", "--json")

	if res.exit != 0 || stub.orgGets != 1 || len(stub.writes) != 2 {
		t.Errorf("exit %d organization requests %d writes %d, want exit 0, 1 and 2", res.exit, stub.orgGets, len(stub.writes))
	}
}

func TestSuitesDeleteWithAnOrgFlagNeverAsksTheServerForOrganizations(t *testing.T) {
	t.Setenv("SELECTIVE_ORG", "")
	stub := &deleteStub{replies: []deleteReply{deleteDone}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--org", "acme", "--json")

	if res.exit != 0 || stub.orgGets != 0 {
		t.Errorf("exit %d organization requests %d, want exit 0 and none", res.exit, stub.orgGets)
	}
}

func TestSuitesDeleteAnExactIdWinsOverAnotherSuiteNamedLikeIt(t *testing.T) {
	const decoyID = "77777777-7777-4777-8777-777777777777"
	decoy := `{"id":"` + decoyID + `","name":"` + archivedSuiteID + `","type":"rspec","repo":"acme/web","archived_at":"2026-11-01T09:00:00Z","last_run_at":null}`
	stub := &deleteStub{replies: []deleteReply{deleteDone}, listing: decoy + "," + archivedSuite}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "suites", "delete", archivedSuiteID, "--yes", "--org", "acme", "--json")

	if res.exit != 0 || len(stub.writes) != 1 || stub.writes[0].path != deleteRoute {
		t.Errorf("exit %d writes %+v, want one DELETE of %s", res.exit, stub.writes, deleteRoute)
	}
}

func TestSuitesDeleteUnderATimeoutWritesToTheCommandsOutputWriter(t *testing.T) {
	stub := &deleteStub{replies: []deleteReply{deleteDone}}
	t.Setenv("SELECTIVE_HOST", stub.start(t).URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())

	code, stdout, _ := execute("suites", "delete", archivedSuiteID, "--yes", "--org", "acme", "--json", "--timeout", "5m")

	if code != 0 || !strings.Contains(stdout, `"result":"deleted"`) {
		t.Errorf("exit %d, stdout %q; want exit 0 and the deleted result on the command's own writer", code, stdout)
	}
}
