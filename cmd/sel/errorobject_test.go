package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func oneObjectUnder512(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(stdout), &object); err != nil || len(stdout) > 512 || strings.Count(stdout, "\n") != 1 || object["v"] != float64(1) {
		t.Fatalf("stdout is %d bytes, not one v1 JSON object of at most 512: %v: %.80q", len(stdout), err, stdout)
	}
	return object
}

func TestTimeoutKeepsEveryJSONErrorObjectUnder512BytesWhateverTheServerSends(t *testing.T) {
	cases := map[string]struct {
		message string
		body    map[string]any
	}{
		"long ascii":            {strings.Repeat("m", 4000), nil},
		"four-byte characters":  {strings.Repeat("😀", 400), nil},
		"escapes that expand":   {strings.Repeat("\"\x01\u2028", 300), nil},
		"oversized extra field": {"short", map[string]any{"big": strings.Repeat("b", 3000), "hint": "kept"}},
		"oversized error code":  {"", map[string]any{"error": strings.Repeat("c", 2000)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"error": "validation_failed", "message": c.message}
			maps.Copy(body, c.body)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(body)
			}))
			t.Cleanup(srv.Close)
			res := runBinaryIn(t, t.TempDir(), srv.URL, t.TempDir(), "approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90", "--json", "--timeout=5s")

			object := oneObjectUnder512(t, res.stdout)
			message, _ := object["message"].(string)
			_, big := object["big"]
			if !utf8.ValidString(message) || !strings.HasPrefix(c.message, message) || big || (c.body["hint"] != nil && object["hint"] != "kept") {
				t.Errorf("object %.200q; want a clean prefix of the message, the oversized field dropped whole and the small one kept", res.stdout)
			}
		})
	}
}

func TestTimeoutMovesDoctorsDocumentToStderrAndLeavesNoTimeoutUntouched(t *testing.T) {
	host := "http://127.0.0.1:1/" + strings.Repeat("h", 3000)
	capped := runBinaryIn(t, checkoutOfAcmeWeb(t), host, t.TempDir(), "doctor", "--json", "--timeout=5s")
	plain := runBinaryIn(t, checkoutOfAcmeWeb(t), host, t.TempDir(), "doctor", "--json")

	object := oneObjectUnder512(t, capped.stdout)
	doctorLine, rest, _ := strings.Cut(capped.stderr, "\n")
	if !json.Valid([]byte(doctorLine)) || !strings.HasPrefix(doctorLine, `{"v":1,"doctor":{`) || len(doctorLine) <= 512 || !strings.HasPrefix(rest, "Error: ") {
		t.Errorf("stderr %d bytes %.100q; want doctor's whole document as one line, then the Error: line", len(doctorLine), capped.stderr)
	}
	if _, has := object["doctor"]; has || object["failed_check"] != "server" || capped.exit != plain.exit {
		t.Errorf("object %v, exit %d against %d; want failed_check server in place of doctor and the same exit", object, capped.exit, plain.exit)
	}
	if len(plain.stdout) <= 512 || !strings.Contains(plain.stdout, `"doctor":{`) || strings.Contains(plain.stderr, `{"v":1`) {
		t.Errorf("without --timeout stdout is %d bytes and stderr %.80q; want doctor's document on stdout as in v0.4.0", len(plain.stdout), plain.stderr)
	}
}

const otherApprovalUUID = "0193b6a0-9999-7d3a-9f2b-4a5c6d7e8f91"

func compactApprovalOf(t *testing.T, approvalJSON string) map[string]any {
	t.Helper()
	var all, kept = map[string]any{}, map[string]any{}
	if err := json.Unmarshal([]byte(approvalJSON), &all); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "url", "status", "expires_at"} {
		kept[key] = all[key]
	}
	return kept
}

func assertLastLineCarries(t *testing.T, exit, wantExit int, lines []string, want map[string]any) {
	t.Helper()
	object := oneObjectUnder512(t, lines[len(lines)-1]+"\n")
	if exit != wantExit || !reflect.DeepEqual(object["approval"], want) {
		t.Errorf("exit %d last line %q; want exit %d and approval %v", exit, lines[len(lines)-1], wantExit, want)
	}
}

func TestTimeoutKeepsACompactApprovalOnEveryExitSixAndSevenErrorObject(t *testing.T) {
	cases := []struct {
		name    string
		replies []reply
		wait    string
		fixture approvalFixture
		exit    int
	}{
		{"denied", []reply{approvalReply(deniedFix)}, "--wait", deniedFix, 6},
		{"expired undecided", []reply{pendingReply, approvalReply(expiredFix)}, "--wait", expiredFix, 7},
		{"expired approved but unused", []reply{approvalReply(unusedFix)}, "--wait", unusedFix, 7},
		{"wait ran out still pending", []reply{pendingReply}, "--wait=5s", pendingApproval, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, lines, _ := runApprovals(t, &stepClock{now: time.Now()}, tc.replies, tc.wait, "--timeout=30s")

			assertLastLineCarries(t, code, tc.exit, lines, compactApprovalOf(t, tc.fixture.json()))
		})
	}
}

func TestTimeoutKeepsTheNewApprovalOnTheErrorWhenTheResendNeedsAnotherOne(t *testing.T) {
	second := strings.ReplaceAll(pendingApproval.json(), approvalUUID, otherApprovalUUID)
	approved := approvalFixture{status: "approved", expiresAt: "2026-11-15T10:00:00.123456Z", decidedAt: "2026-11-14T10:05:00Z"}
	again := deleteReply{202, `{"result":"pending_approval","dry_run":false,"approval":` + second + `}`}
	stub := &deleteStub{replies: []deleteReply{deletePending, again}, approvals: []string{pendingApproval.json(), approved.json()}}
	t.Setenv("SELECTIVE_HOST", stub.start(t).URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	useClock(t, &stepClock{now: time.Now()})

	code, stdout, _ := execute("suites", "delete", archivedSuiteID, "--yes", "--org", "acme", "--wait=2m", "--json", "--timeout=30s")

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	assertLastLineCarries(t, code, 7, lines, compactApprovalOf(t, second))
}

func TestTimeoutKeepsTheSmallFieldsAnAgentActsOnBesideALongServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden", "message": strings.Repeat("m", 500), "scope": "repo:acme/web"})
	}))
	t.Cleanup(srv.Close)

	res := runBinaryIn(t, t.TempDir(), srv.URL, t.TempDir(), "approvals", "show", approvalUUID, "--json", "--timeout=5s")

	object := oneObjectUnder512(t, res.stdout)
	if object["retry_after_s"] != float64(30) || object["scope"] != "repo:acme/web" {
		t.Errorf("object %.200q; want retry_after_s 30 and the scope kept beside the long message", res.stdout)
	}
}
