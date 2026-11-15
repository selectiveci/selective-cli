package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

type approvalsListStub struct {
	mu      sync.Mutex
	paths   []string
	queries []url.Values
	orgs    string
	status  int
	body    string
	pages   map[string]string
}

func (s *approvalsListStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/orgs" {
			fmt.Fprint(w, s.orgs)
			return
		}
		s.paths = append(s.paths, r.URL.Path)
		s.queries = append(s.queries, r.URL.Query())
		if s.status != 0 {
			w.WriteHeader(s.status)
			fmt.Fprint(w, s.body)
			return
		}
		fmt.Fprint(w, s.pages[r.URL.Query().Get("cursor")])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func listPage(rows string, next string) string {
	return `{"approvals":[` + rows + `],"next_cursor":` + next + `}`
}

var (
	deniedApproval = approvalFixture{status: "denied", expiresAt: "2026-11-15T10:00:00Z", decidedAt: "2026-11-14T11:00:00Z", note: "no"}
	oneOrg         = `{"organizations":[{"slug":"acme"}]}`
	twoOrgs        = `{"organizations":[{"slug":"acme"},{"slug":"globex"}]}`
)

func TestApprovalsListWalksEveryPageAndPrintsOneDocument(t *testing.T) {
	first, second := pendingApproval.json(), deniedApproval.json()
	stub := &approvalsListStub{orgs: oneOrg, pages: map[string]string{"": listPage(first, `"page-2"`), "page-2": listPage(second, "null")}}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "approvals", "list", "--json")

	want := `{"v":1,"approvals":[` + first + `,` + second + `],"next_cursor":null}` + "\n"
	if res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q stderr %q", res.exit, res.stdout, want, res.stderr)
	}
	var cursors, limits []string
	for _, q := range stub.queries {
		cursors, limits = append(cursors, q.Get("cursor")), append(limits, q.Get("limit"))
	}
	if strings.Join(cursors, ",") != ",page-2" || strings.Join(limits, ",") != "200,200" || !slices.Equal(stub.paths, []string{"/api/v1/orgs/acme/approvals", "/api/v1/orgs/acme/approvals"}) {
		t.Errorf("cursors %q limits %q paths %q", cursors, limits, stub.paths)
	}
}

func TestApprovalsListSendsTheStatusFilterAndRefusesAnUnknownOneLocally(t *testing.T) {
	stub := &approvalsListStub{orgs: oneOrg, pages: map[string]string{"": listPage("", "null")}}
	srv := stub.start(t)

	ok := runBinary(t, t.TempDir(), srv.URL, "approvals", "list", "--status", "denied", "--json")
	sent := len(stub.queries)
	bad := runBinary(t, t.TempDir(), srv.URL, "approvals", "list", "--status", "done", "--json")

	if ok.exit != 0 || sent != 1 || stub.queries[0].Get("status") != "denied" {
		t.Errorf("exit %d queries %v, want one request with status=denied", ok.exit, stub.queries)
	}
	assertRefusal(t, bad, 5, "usage_error")
	if len(stub.queries) != sent {
		t.Errorf("an unknown status sent %d more requests, want none", len(stub.queries)-sent)
	}
}

func TestApprovalsListSendsNoStatusByDefault(t *testing.T) {
	stub := &approvalsListStub{orgs: oneOrg, pages: map[string]string{"": listPage("", "null")}}
	srv := stub.start(t)

	runBinary(t, t.TempDir(), srv.URL, "approvals", "list", "--json")

	if len(stub.queries) != 1 || stub.queries[0].Has("status") {
		t.Errorf("queries %v, want one request with no status", stub.queries)
	}
}

func TestApprovalsListMapsServerRefusalsToExitCodes(t *testing.T) {
	forbidden := `{"error":"forbidden","message":"Only an organization owner or admin can list approvals."}`
	noOrg := `{"error":"organization_not_found","message":"No such organization."}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		exit   int
	}{{"forbidden", 403, forbidden, 6}, {"organization not found", 404, noOrg, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&approvalsListStub{orgs: oneOrg, status: tc.status, body: tc.body}).start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "approvals", "list", "--json")

			if want := `{"v":1,` + strings.TrimPrefix(tc.body, "{") + "\n"; res.exit != tc.exit || res.stdout != want {
				t.Errorf("exit %d stdout %q, want %d and %q", res.exit, res.stdout, tc.exit, want)
			}
		})
	}
}

func TestApprovalsListPicksTheOrgFromTheFlagThenTheEnvironmentThenTheOnlyOne(t *testing.T) {
	cases := []struct {
		name, orgs, env string
		args            []string
		wantPath        string
	}{
		{"flag beats environment", twoOrgs, "globex", []string{"--org", "acme"}, "/api/v1/orgs/acme/approvals"},
		{"environment beats the only org", oneOrg, "globex", nil, "/api/v1/orgs/globex/approvals"},
		{"the only org", oneOrg, "", nil, "/api/v1/orgs/acme/approvals"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SELECTIVE_ORG", tc.env)
			stub := &approvalsListStub{orgs: tc.orgs, pages: map[string]string{"": listPage("", "null")}}
			srv := stub.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append([]string{"approvals", "list", "--json"}, tc.args...)...)

			if res.exit != 0 || !slices.Equal(stub.paths, []string{tc.wantPath}) {
				t.Errorf("exit %d paths %v, want %s", res.exit, stub.paths, tc.wantPath)
			}
		})
	}
	t.Run("two orgs and none named", func(t *testing.T) {
		t.Setenv("SELECTIVE_ORG", "")
		stub := &approvalsListStub{orgs: twoOrgs}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "approvals", "list", "--json")

		assertRefusal(t, res, 5, "usage_error")
		if len(stub.paths) != 0 {
			t.Errorf("paths %v, want no approvals request", stub.paths)
		}
	})
}

func TestHelpListsApprovalsListAsAReadWithTheTokensExitCodes(t *testing.T) {
	assertReadHelp(t, "approvals list", []int{0, 3, 4, 5, 6}, nil, "")
}
