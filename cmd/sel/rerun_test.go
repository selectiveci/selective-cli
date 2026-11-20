package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type rerunCall struct {
	method, path, query, key, body string
}

type stubReply struct {
	status     int
	body       string
	retryAfter string
}

const (
	rerunCreated = `{"build_id":"b1","rerun":{"enqueued":2,"runs":[{"id":"r1","enqueued":2}]},"result":"created","dry_run":false}`
	rerunKeyForm = `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
)

func rerunOnce(t *testing.T, replies []stubReply, args ...string) (cliResult, []rerunCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []rerunCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, rerunCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Idempotency-Key"), string(body)})
		reply := replies[min(len(calls), len(replies))-1]
		if reply.retryAfter != "" {
			w.Header().Set("Retry-After", reply.retryAfter)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	res := runBinary(t, t.TempDir(), srv.URL, append([]string{"rerun"}, args...)...)
	mu.Lock()
	defer mu.Unlock()
	return res, calls
}

func TestRerunFailedPostsFailedModeToTheBuildAndRelaysTheWriteObject(t *testing.T) {
	res, calls := rerunOnce(t, []stubReply{{status: 201, body: rerunCreated}}, "--failed", "b1", "--org", "acme", "--json")

	want := `{"v":1,` + strings.TrimPrefix(rerunCreated, "{") + "\n"
	if len(calls) != 1 || calls[0].method != "POST" || calls[0].path != "/api/v1/builds/b1/rerun" || calls[0].query != "org=acme" || calls[0].body != `{"mode":"failed"}` {
		t.Errorf("requests = %+v, want one POST /api/v1/builds/b1/rerun?org=acme with the failed mode", calls)
	}
	if res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q, want 0 and %q", res.exit, res.stdout, want)
	}
}

func TestRerunDryRunSendsTheQueryAndRelaysTheDryRunObject(t *testing.T) {
	dry := strings.Replace(rerunCreated, `"dry_run":false`, `"dry_run":true`, 1)
	res, calls := rerunOnce(t, []stubReply{{status: 200, body: dry}}, "--failed", "b1", "--dry-run", "--json")

	if len(calls) != 1 || calls[0].query != "dry_run=true" {
		t.Errorf("requests = %+v, want one request with ?dry_run=true", calls)
	}
	if res.exit != 0 || !strings.Contains(res.stdout, `"dry_run":true}`) {
		t.Errorf("exit %d stdout %q, want 0 and the dry run object", res.exit, res.stdout)
	}
}

func TestRerunSendsARandomKeyPerInvocation(t *testing.T) {
	keys := map[string]bool{}
	for range 2 {
		_, calls := rerunOnce(t, []stubReply{{status: 201, body: rerunCreated}}, "--failed", "b1", "--json")
		if len(calls) != 1 || !regexp.MustCompile(rerunKeyForm).MatchString(calls[0].key) {
			t.Fatalf("requests = %+v, want one carrying a UUIDv4 Idempotency-Key", calls)
		}
		keys[calls[0].key] = true
	}
	if len(keys) != 2 {
		t.Errorf("two invocations sent keys %v, want two different ones", keys)
	}
}

func TestRerunRetriesAServerErrorWithTheSameKey(t *testing.T) {
	res, calls := rerunOnce(t, []stubReply{
		{status: 503, retryAfter: "0", body: `{"error":"server_error","message":"The run did not answer in time."}`},
		{status: 201, body: rerunCreated},
	}, "--failed", "b1", "--json")

	if len(calls) != 2 || calls[0].key == "" || calls[0].key != calls[1].key {
		t.Errorf("requests = %+v, want two carrying the same non-empty key", calls)
	}
	if res.exit != 0 {
		t.Errorf("exit %d stdout %q, want 0", res.exit, res.stdout)
	}
}

func TestRerunIdempotencyKeyFlagSendsThatKey(t *testing.T) {
	_, calls := rerunOnce(t, []stubReply{{status: 200, body: rerunCreated}}, "--failed", "b1", "--idempotency-key", "agent-key-1", "--json")

	if len(calls) != 1 || calls[0].key != "agent-key-1" {
		t.Errorf("requests = %+v, want one with key agent-key-1", calls)
	}
}

func TestRerunRefusesBeforeSendingAnything(t *testing.T) {
	cases := map[string]struct {
		args    []string
		mention string
	}{
		"no --failed":  {[]string{"b1", "--json"}, "--failed"},
		"an empty key": {[]string{"--failed", "b1", "--idempotency-key", "", "--json"}, "--idempotency-key"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, calls := rerunOnce(t, []stubReply{{status: 201, body: rerunCreated}}, tc.args...)

			assertRefusal(t, res, 5, "usage_error")
			if !strings.Contains(res.stdout, tc.mention) || len(calls) != 0 {
				t.Errorf("stdout %q with %d requests, want it to mention %q and no requests", res.stdout, len(calls), tc.mention)
			}
		})
	}
}

func TestRerunMapsServerAnswersToExitCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		exit   int
		code   string
	}{
		{409, `{"error":"run_not_running","message":"No run of this build is live."}`, 5, "run_not_running"},
		{409, `{"error":"request_in_progress","message":"Still running.","retry_after_s":1}`, 4, "request_in_progress"},
		{422, `{"error":"idempotency_key_reused","message":"Different request."}`, 5, "idempotency_key_reused"},
		{404, `{"error":"not_found","message":"No build with this id."}`, 3, "not_found"},
		{403, `{"error":"insufficient_scope","message":"Needs write.","scope":"write"}`, 6, "insufficient_scope"},
		{202, `{"result":"pending_approval","dry_run":false,"approval":{"id":"a1","url":"http://x/a1","status":"pending","expires_at":"2026-11-15T10:00:00Z"}}`, 7, "pending_approval"},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status)+" "+tc.code, func(t *testing.T) {
			res, _ := rerunOnce(t, []stubReply{{status: tc.status, body: tc.body}}, "--failed", "b1", "--json")

			if res.exit != tc.exit || !strings.Contains(res.stdout, `"`+tc.code+`"`) {
				t.Errorf("exit %d stdout %q, want exit %d with %s", res.exit, res.stdout, tc.exit, tc.code)
			}
		})
	}
}

func TestHelpDeclaresRerunAsANonDestructiveWrite(t *testing.T) {
	_, stdout, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path        string   `json:"path"`
				ExitCodes   []int    `json:"exit_codes"`
				Events      []string `json:"events"`
				WaitDefault string   `json:"wait_default"`
				Mutates     bool     `json:"mutates"`
				Destructive bool     `json:"destructive"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Help.Commands {
		if c.Path == "rerun" {
			if !c.Mutates || c.Destructive || len(c.ExitCodes) != 9 || c.ExitCodes[8] != 8 || c.WaitDefault != "10m" || !strings.Contains(strings.Join(c.Events, ","), "test_fail,summary") {
				t.Errorf("rerun = %+v, want mutates, not destructive, exit codes 0-8, wait_default 10m and the tail events", c)
			}
			return
		}
	}
	t.Error("help --json has no rerun command")
}
