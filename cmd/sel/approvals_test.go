package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	approvalUUID      = "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"
	approvalURL       = "https://selective.example/acme/approvals/" + approvalUUID
	approvalNotFound  = `{"error":"not_found","message":"No such approval."}`
	approvalNotFoundO = `{"v":1,"error":"not_found","message":"No such approval."}` + "\n"
)

var approvalKeys = []string{"id", "url", "status", "action", "subject", "params", "requested_by", "expires_at", "decided_at", "decided_by_user_id", "decision_note", "consumed_at", "inserted_at"}

type approvalFixture struct {
	status, expiresAt, decidedAt, note, consumedAt string
}

func (f approvalFixture) json() string {
	orNull := func(s string) string {
		if s == "" {
			return "null"
		}
		return fmt.Sprintf("%q", s)
	}
	decider := "null"
	if f.decidedAt != "" {
		decider = `"0193b6a0-0000-7000-8000-00000000aaaa"`
	}
	return fmt.Sprintf(`{"id":%q,"url":%q,"status":%q,"action":"suite.delete","subject":{"type":"suite","id":"suite-1","label":"web-rspec"},"params":{},"requested_by":{"token_id":"0193b6a0-0000-7000-8000-00000000bbbb","token_name":"ci-agent","user_id":"0193b6a0-0000-7000-8000-00000000cccc"},"expires_at":%s,"decided_at":%s,"decided_by_user_id":%s,"decision_note":%s,"consumed_at":%s,"inserted_at":"2026-11-14T10:00:00.123456Z"}`,
		approvalUUID, approvalURL, f.status, orNull(f.expiresAt), orNull(f.decidedAt), decider, orNull(f.note), orNull(f.consumedAt))
}

var pendingApproval = approvalFixture{status: "pending", expiresAt: "2026-11-15T10:00:00.123456Z"}

func TestApprovalsShowPrintsTheServersObjectWithAllThirteenKeys(t *testing.T) {
	body := pendingApproval.json()
	stub := &revokeStub{status: 200, body: `{"approval":` + body + `}`}
	srv := stub.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "approvals", "show", approvalUUID, "--json")

	if want := `{"v":1,"approval":` + body + `}` + "\n"; res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q stderr %q", res.exit, res.stdout, want, res.stderr)
	}
	var doc struct {
		Approval map[string]json.RawMessage `json:"approval"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range doc.Approval {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := slices.Sorted(slices.Values(approvalKeys)); !slices.Equal(keys, want) {
		t.Errorf("approval keys = %v, want the server's %v", keys, want)
	}
	if want := []string{"GET /api/v1/approvals/" + approvalUUID}; !slices.Equal(stub.requests, want) {
		t.Errorf("requests = %v, want %v", stub.requests, want)
	}
}

func TestApprovalsShowOfAnUnknownOrInvisibleApprovalExitsThreeWithTheServersError(t *testing.T) {
	srv := (&revokeStub{status: 404, body: approvalNotFound}).start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "approvals", "show", approvalUUID, "--json")

	if res.exit != 3 || res.stdout != approvalNotFoundO {
		t.Errorf("exit %d stdout %q want 3 and %q", res.exit, res.stdout, approvalNotFoundO)
	}
}

func TestApprovalsShowRejectsAnyIDThatIsNotAUUIDBeforeSendingAnything(t *testing.T) {
	for _, id := range []string{"not-a-uuid", "", "../x", "current", approvalUUID + "/extra"} {
		stub := &revokeStub{status: 200, body: `{}`}
		srv := stub.start(t)

		res := runBinary(t, t.TempDir(), srv.URL, "approvals", "show", id, "--json")

		assertRefusal(t, res, 5, "usage_error")
		if len(stub.requests) != 0 || !strings.Contains(res.stdout, "is not an approval id") {
			t.Errorf("show %q sent %v stdout %q, want no request and the approval-id message", id, stub.requests, res.stdout)
		}
	}
}

func TestApprovalsShowCardStatesEachStatusAndPrintsTheURL(t *testing.T) {
	cases := []struct {
		name    string
		fixture approvalFixture
		want    []string
	}{
		{"pending", pendingApproval, []string{"Status:     pending, expires 2026-11-15 10:00 UTC"}},
		{"approved unused", approvalFixture{status: "approved", decidedAt: "2026-11-14T11:00:00Z", expiresAt: "2026-11-14T12:00:00Z"}, []string{"Status:     approved 2026-11-14 11:00 UTC, valid until 2026-11-14 12:00 UTC, not used yet"}},
		{"approved used", approvalFixture{status: "approved", decidedAt: "2026-11-14T11:00:00Z", expiresAt: "2026-11-14T12:00:00Z", consumedAt: "2026-11-14T11:30:00Z"}, []string{"Status:     approved 2026-11-14 11:00 UTC, valid until 2026-11-14 12:00 UTC, used 2026-11-14 11:30 UTC"}},
		{"denied with a note", approvalFixture{status: "denied", decidedAt: "2026-11-14T11:00:00Z", expiresAt: "2026-11-15T10:00:00Z", note: "wrong suite"}, []string{"Status:     denied 2026-11-14 11:00 UTC", "Note:       wrong suite"}},
		{"expired", approvalFixture{status: "expired", expiresAt: "2026-11-15T10:00:00Z"}, []string{"Status:     expired 2026-11-15 10:00 UTC"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := (&revokeStub{status: 200, body: `{"approval":` + tc.fixture.json() + `}`}).start(t)
			var out, errOut bytes.Buffer

			if err := approvalsShow(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}, approvalUUID); err != nil {
				t.Fatal(err)
			}

			for _, want := range append(tc.want, "suite.delete web-rspec\n", "Requested:  by ci-agent", "URL:        "+approvalURL) {
				if !strings.Contains(out.String(), want) {
					t.Errorf("card missing %q:\n%s", want, out.String())
				}
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want empty", errOut.String())
			}
		})
	}
}

func TestHelpListsApprovalsShowAsAReadWithTheTokensExitCodes(t *testing.T) {
	assertReadExitCodes(t, "approvals show")
}

func assertReadExitCodes(t *testing.T, path string) {
	t.Helper()
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
	for _, c := range doc.Help.Commands {
		if c.Path == path {
			if !slices.Equal(c.ExitCodes, []int{0, 3, 4, 5, 6}) || c.Mutates {
				t.Errorf("%s = %+v, want exit codes 0,3,4,5,6 and no mutates", path, c)
			}
			return
		}
	}
	t.Errorf("help --json lists no %s", path)
}

func TestApprovalsShowCardNamesTheSubjectByTypeAndIDWhenItHasNoLabel(t *testing.T) {
	body := strings.Replace(pendingApproval.json(), `"label":"web-rspec"`, `"label":null`, 1)
	srv := (&revokeStub{status: 200, body: `{"approval":` + body + `}`}).start(t)
	var out bytes.Buffer

	if err := approvalsShow(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &bytes.Buffer{}}, approvalUUID); err != nil {
		t.Fatal(err)
	}

	if want := "suite.delete suite suite-1\n"; !strings.HasPrefix(out.String(), want) {
		t.Errorf("card = %q, want it to start with %q", out.String(), want)
	}
}
