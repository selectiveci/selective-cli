package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
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

			if err := approvalsShow(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}, approvalUUID, showOptions{}); err != nil {
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
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path        string   `json:"path"`
				ExitCodes   []int    `json:"exit_codes"`
				Mutates     bool     `json:"mutates"`
				Events      []string `json:"events"`
				WaitDefault string   `json:"wait_default"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Help.Commands {
		if c.Path == "approvals show" {
			if !slices.Equal(c.ExitCodes, []int{0, 3, 4, 5, 6, 7, 8}) || !slices.Equal(c.Events, []string{"waiting", "approved"}) || c.WaitDefault != "10m" || c.Mutates {
				t.Errorf("approvals show = %+v, want exit codes 0,3,4,5,6,7,8, events waiting+approved, wait_default 10m and no mutates", c)
			}
			return
		}
	}
	t.Error("help --json lists no approvals show")
}

func TestApprovalsShowCardNamesTheSubjectByTypeAndIDWhenItHasNoLabel(t *testing.T) {
	body := strings.Replace(pendingApproval.json(), `"label":"web-rspec"`, `"label":null`, 1)
	srv := (&revokeStub{status: 200, body: `{"approval":` + body + `}`}).start(t)
	var out bytes.Buffer

	if err := approvalsShow(&setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &bytes.Buffer{}}, approvalUUID, showOptions{}); err != nil {
		t.Fatal(err)
	}

	if want := "suite.delete suite suite-1\n"; !strings.HasPrefix(out.String(), want) {
		t.Errorf("card = %q, want it to start with %q", out.String(), want)
	}
}

type stepClock struct {
	now   time.Time
	block bool
}

func (c *stepClock) Now() time.Time { return c.now }

func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	if c.block {
		<-ctx.Done()
		return ctx.Err()
	}
	c.now = c.now.Add(d)
	return ctx.Err()
}

type reply struct {
	status int
	body   string
	delay  time.Duration
}

func approvalReply(f approvalFixture) reply {
	return reply{status: 200, body: `{"approval":` + f.json() + `}`}
}

var (
	pendingReply  = approvalReply(pendingApproval)
	approvedFix   = approvalFixture{status: "approved", decidedAt: "2026-11-14T11:00:00Z", expiresAt: "2026-11-14T12:00:00Z"}
	deniedFix     = approvalFixture{status: "denied", decidedAt: "2026-11-14T11:00:00Z", expiresAt: "2026-11-15T10:00:00Z", note: "wrong suite"}
	expiredFix    = approvalFixture{status: "expired", expiresAt: "2026-11-15T10:00:00Z"}
	unusedFix     = approvalFixture{status: "expired", decidedAt: "2026-11-14T11:00:00Z", expiresAt: "2026-11-14T12:00:00Z"}
	unavailable   = reply{status: 503, body: `{"error":"server_error","message":"down"}`}
	approvalsGone = reply{status: 404, body: approvalNotFound}
)

func replyServer(t *testing.T, replies []reply, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rep := replies[min(int(requests.Add(1))-1, len(replies)-1)]
		time.Sleep(rep.delay)
		w.WriteHeader(rep.status)
		fmt.Fprint(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useClock(t *testing.T, clock *stepClock) {
	t.Helper()
	t.Cleanup(api.SetClock(&stepClock{}))
	approvalsClock = clock
	t.Cleanup(func() { approvalsClock = nil })
}

func runApprovals(t *testing.T, clock *stepClock, replies []reply, args ...string) (code int, lines []string, requests int) {
	t.Helper()
	var count atomic.Int32
	srv := replyServer(t, replies, &count)
	t.Setenv("SELECTIVE_HOST", srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	useClock(t, clock)

	code, stdout, _ := execute(append([]string{"approvals", "show", approvalUUID, "--json"}, args...)...)

	return code, strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"), int(count.Load())
}

func eventLine(event, approval string) string {
	return `{"v":1,"event":"` + event + `","approval":` + approval + `}`
}

func errorLine(code, message, approval string) string {
	return `{"v":1,"error":"` + code + `","message":"` + message + `","approval":` + approval + `}`
}

func TestApprovalsShowWaitEndsWithOneTerminalLineAndTheExitCodeOfTheOutcome(t *testing.T) {
	approved, denied, expired, unused := approvedFix.json(), deniedFix.json(), expiredFix.json(), unusedFix.json()
	cases := []struct {
		name    string
		replies []reply
		args    []string
		exit    int
		count   int
		last    string
	}{
		{"approved after two polls", []reply{pendingReply, pendingReply, approvalReply(approvedFix)}, []string{"--wait"}, 0, 3, eventLine("approved", approved)},
		{"denied with a note", []reply{pendingReply, approvalReply(deniedFix)}, []string{"--wait"}, 6, 2, errorLine("approval_denied", "approval denied: wrong suite", denied)},
		{"pending lapsed", []reply{pendingReply, approvalReply(expiredFix)}, []string{"--wait"}, 7, 2, errorLine("approval_expired", "approval expired before anyone decided it; request the action again", expired)},
		{"approved but never used", []reply{approvalReply(unusedFix)}, []string{"--wait"}, 7, 1, errorLine("approval_expired", "approval was approved at 2026-11-14T11:00:00Z but not used before it expired; request the action again", unused)},
		{"already approved", []reply{approvalReply(approvedFix)}, []string{"--wait"}, 0, 1, eventLine("approved", approved)},
		{"already denied", []reply{approvalReply(deniedFix)}, []string{"--wait"}, 6, 1, errorLine("approval_denied", "approval denied: wrong suite", denied)},
		{"wait runs out pending", []reply{pendingReply}, []string{"--wait=5s"}, 7, 3, errorLine("pending_approval", "still pending after 5s; a person must decide it at "+approvalURL, pendingApproval.json())},
		{"transient errors do not end the wait", []reply{unavailable, unavailable, unavailable, approvalReply(approvedFix)}, []string{"--wait"}, 0, 2, eventLine("approved", approved)},
		{"wait ends on a transient error", []reply{unavailable}, []string{"--wait=5s"}, 4, 3, `{"v":1,"error":"server_error","message":"down","attempts":3}`},
		{"a 404 mid-wait passes through", []reply{pendingReply, approvalsGone}, []string{"--wait"}, 3, 2, approvalNotFoundO[:len(approvalNotFoundO)-1]},
		{"plain show of a denied approval is a read", []reply{approvalReply(deniedFix)}, nil, 0, 1, `{"v":1,"approval":` + denied + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, lines, _ := runApprovals(t, &stepClock{now: time.Now()}, tc.replies, tc.args...)

			if code != tc.exit || len(lines) != tc.count || lines[len(lines)-1] != tc.last {
				t.Errorf("exit %d, %d lines, last %q; want exit %d, %d lines, last %q", code, len(lines), lines[len(lines)-1], tc.exit, tc.count, tc.last)
			}
			previous := 0
			for _, line := range lines[:len(lines)-1] {
				var event struct {
					Event    string `json:"event"`
					ElapsedS int    `json:"elapsed_s"`
					For      string `json:"for"`
				}
				if err := json.Unmarshal([]byte(line), &event); err != nil || event.Event != "waiting" || event.For != "approval" || event.ElapsedS < previous {
					t.Errorf("line %q is not a waiting event for approval with elapsed_s >= %d", line, previous)
				}
				previous = event.ElapsedS
			}
		})
	}
}

func TestApprovalsShowTimeoutCapsTheWaitAndAPlainRead(t *testing.T) {
	cases := map[string]struct {
		replies []reply
		args    []string
		count   int
	}{
		"waiting":    {[]reply{pendingReply}, []string{"--wait=1m", "--timeout=100ms"}, 2},
		"plain read": {[]reply{{status: 200, body: `{}`, delay: time.Second}}, []string{"--timeout=100ms"}, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, lines, _ := runApprovals(t, &stepClock{now: time.Now(), block: true}, tc.replies, tc.args...)

			if want := `{"v":1,"error":"timeout","message":"--timeout 100ms expired"}`; code != 8 || len(lines) != tc.count || lines[len(lines)-1] != want {
				t.Errorf("exit %d, %d lines, last %q; want exit 8, %d lines, last %q", code, len(lines), lines[len(lines)-1], tc.count, want)
			}
		})
	}
}

func TestApprovalsShowRefusesANonPositiveWaitOrTimeoutBeforeSendingAnything(t *testing.T) {
	for _, name := range []string{"wait", "timeout"} {
		for _, value := range []string{"0", "-1s"} {
			code, lines, requests := runApprovals(t, &stepClock{now: time.Now()}, []reply{pendingReply}, "--"+name+"="+value)

			want := `{"v":1,"error":"usage_error","message":"--` + name + ` must be a positive duration"}`
			if code != 5 || requests != 0 || len(lines) != 1 || lines[0] != want {
				t.Errorf("--%s=%s: exit %d, %d requests, stdout %q; want exit 5, no request and %q", name, value, code, requests, lines, want)
			}
		}
	}
}

func TestApprovalsShowWaitInTextModeKeepsWaitingLinesOffStdoutAndPrintsTheCard(t *testing.T) {
	now := time.Now()
	var approved api.Approval
	if err := json.Unmarshal([]byte(approvedFix.json()), &approved); err != nil {
		t.Fatal(err)
	}
	var card bytes.Buffer
	renderApproval(&card, &approved)
	cases := []struct {
		name    string
		replies []reply
		exit    int
		stdout  string
		message string
	}{
		{"approved", []reply{pendingReply, approvalReply(approvedFix)}, 0, card.String(), ""},
		{"denied", []reply{pendingReply, approvalReply(deniedFix)}, 6, "", "approval denied: wrong suite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.replies, &atomic.Int32{})
			useClock(t, &stepClock{now: now})
			var out, errOut bytes.Buffer
			env := &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}

			err := approvalsShow(env, approvalUUID, showOptions{wait: time.Hour, started: now})

			code, message := 0, ""
			var coded contract.Coded
			if errors.As(err, &coded) {
				code, message = coded.ExitCode(), err.Error()
			}
			if code != tc.exit || message != tc.message || out.String() != tc.stdout || errOut.String() != "waiting for approval (0s)\n" {
				t.Errorf("exit %d message %q stdout %q stderr %q; want exit %d, message %q, stdout %q and one pretty waiting line on stderr", code, message, out.String(), errOut.String(), tc.exit, tc.message, tc.stdout)
			}
		})
	}
}
