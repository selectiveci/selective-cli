package main

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

type budgetServer struct {
	mu       sync.Mutex
	log      []string
	keys     []string
	bodies   []string
	queries  []string
	replies  []stubReply
	frames   []string
	lookups  []int
	postHang time.Duration
}

const budgetWritten = `{"build_id":"b1","retry_budget":{"max_retries":3,"runs":[{"id":"r1","applied":true,"max_retries":3,"requeued_test_ids":[]}]},"result":"updated","dry_run":false}`

func (s *budgetServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/builds/lookup":
			status := http.StatusOK
			if len(s.log) < len(s.lookups) {
				status = s.lookups[len(s.log)]
			}
			s.log = append(s.log, "lookup")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"build_id":"b1","status":"running"}`))
		case "/api/v1/builds/b1/retry_budget":
			body, _ := io.ReadAll(r.Body)
			s.log = append(s.log, r.Method+" retry_budget")
			s.keys = append(s.keys, r.Header.Get("Idempotency-Key"))
			s.bodies = append(s.bodies, string(body))
			s.queries = append(s.queries, r.URL.RawQuery)
			if s.postHang > 0 {
				s.mu.Unlock()
				select {
				case <-time.After(s.postHang):
				case <-r.Context().Done():
				}
				s.mu.Lock()
			}
			reply := s.replies[min(len(s.keys), len(s.replies))-1]
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
		case "/api/v1/tail/websocket":
			s.log = append(s.log, "tail")
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			ctx := c.CloseRead(context.Background())
			for _, f := range s.frames {
				if c.Write(ctx, websocket.MessageText, []byte(f)) != nil {
					return
				}
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *budgetServer) requests() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.log, ",")
}

func TestTailRetriesOutsideOneToTenExitsFiveBeforeAnyRequest(t *testing.T) {
	for _, value := range []string{"11", "0", "-1", "abc"} {
		t.Run(value, func(t *testing.T) {
			s := &budgetServer{replies: []stubReply{{status: 422, body: `{"error":"invalid_request","message":"max_retries must be between 1 and 10"}`}}}
			srv := s.start(t)

			res := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--retries="+value, "--json")

			assertRefusal(t, res, 5, "usage_error")
			want := "--retries must be between 1 and 10 (got " + value + ")"
			if !strings.Contains(res.stdout, want) {
				t.Errorf("stdout %q lacks %q", res.stdout, want)
			}
			if got := s.requests(); got != "" {
				t.Errorf("requests = %q, want none", got)
			}
		})
	}
}

func TestTailRetriesSetsTheBudgetBeforeTheStreamAndKeepsTailsExitCode(t *testing.T) {
	cases := []struct {
		name   string
		frames []string
		exit   int
	}{
		{"green", []string{frameStart, summaryPass}, 0},
		{"flaky", []string{frameStart, frameFail, summaryFlaky}, 2},
		{"failing", []string{frameStart, frameFail, summaryFail}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &budgetServer{frames: tc.frames, replies: []stubReply{{status: 200, body: budgetWritten}}}
			srv := s.start(t)

			res := runBinary(t, gitRepo(t), srv.URL, "tail", "--retries", "3", "--json")

			want := `{"v":1,` + strings.TrimPrefix(budgetWritten, "{") + "\n" + strings.Join(tc.frames, "\n")
			if res.exit != tc.exit || res.stdout != want+"\n" {
				t.Errorf("exit %d stdout %q\nwant exit %d stdout %q", res.exit, res.stdout, tc.exit, want)
			}
			if got := s.requests(); got != "lookup,POST retry_budget,tail" || s.bodies[0] != `{"max_retries":3}` {
				t.Errorf("requests = %q body %v, want lookup, the POST of 3, then the stream", got, s.bodies)
			}
		})
	}
}

func TestTailRetriesRefusalsAttachNoStreamAndEndOnTheErrorObject(t *testing.T) {
	cases := []struct {
		status int
		code   string
		exit   int
	}{
		{409, "run_not_running", 5},
		{403, "insufficient_scope", 6},
		{422, "invalid_request", 5},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			body := `{"error":"` + tc.code + `","message":"refused"}`
			s := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: tc.status, body: body}}}
			srv := s.start(t)

			res := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--retries", "3", "--json")

			assertRefusal(t, res, tc.exit, tc.code)
			lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
			var last struct{ Error string }
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.Error != tc.code || len(lines) != 1 {
				t.Errorf("stdout %q, want the error object as the only line", res.stdout)
			}
			if got := s.requests(); got != "POST retry_budget" {
				t.Errorf("requests = %q, want the POST and no stream", got)
			}
		})
	}
}

func TestTailRetriesReusesOneIdempotencyKeyWhenThePostIsRetried(t *testing.T) {
	s := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 503, body: `{"error":"server_error","message":"busy"}`}, {status: 200, body: budgetWritten}}}
	srv := s.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--retries", "3", "--json")

	if res.exit != 0 || len(s.keys) != 2 || s.keys[0] != s.keys[1] || s.keys[0] == "" {
		t.Errorf("exit %d keys %q, want exit 0 and the same non-empty key twice", res.exit, s.keys)
	}
	explicit := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: budgetWritten}}}
	runBinary(t, gitRepo(t), explicit.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--idempotency-key", "k-1", "--json")
	if len(explicit.keys) != 1 || explicit.keys[0] != "k-1" {
		t.Errorf("keys = %q, want [k-1]", explicit.keys)
	}
}

func TestTailWithoutRetriesSendsNoWriteAndRejectsAnIdempotencyKey(t *testing.T) {
	s := &budgetServer{frames: []string{summaryPass}}
	srv := s.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--json")
	keyed := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--idempotency-key", "k", "--json")

	if got := s.requests(); res.exit != 0 || got != "tail" {
		t.Errorf("exit %d requests %q, want exit 0 and only the stream", res.exit, got)
	}
	dry := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--dry-run", "--json")
	assertRefusal(t, keyed, 5, "usage_error")
	assertRefusal(t, dry, 5, "usage_error")
}

func TestTailDeclaresThatItMutatesOnlyWithRetries(t *testing.T) {
	_, out, _ := execute("help", "tail", "--json")

	var doc struct {
		Command struct {
			Mutates     bool   `json:"mutates"`
			MutatesWith string `json:"mutates_with"`
			Flags       []struct{ Name string }
		}
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, f := range doc.Command.Flags {
		flags[f.Name] = true
	}
	if doc.Command.Mutates || doc.Command.MutatesWith != "retries" || !flags["retries"] || !flags["idempotency-key"] {
		t.Errorf("tail = %+v, want mutates false, mutates_with retries, and the retries and idempotency-key flags", doc.Command)
	}
}

func TestTailRetriesNotesAnUnappliedRunOrADifferentBudgetOnStderrWithoutChangingTheExit(t *testing.T) {
	body := `{"build_id":"b1","retry_budget":{"max_retries":3,"runs":[{"id":"r1","applied":false,"max_retries":null,"requeued_test_ids":[]},{"id":"r2","applied":true,"max_retries":5,"requeued_test_ids":[]}]},"result":"updated","dry_run":false}`
	s := &budgetServer{frames: []string{frameStart, summaryPass}, replies: []stubReply{{status: 200, body: body}}}

	res := runBinary(t, gitRepo(t), s.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--json")

	first, _, _ := strings.Cut(res.stdout, "\n")
	if res.exit != 0 || first != `{"v":1,`+strings.TrimPrefix(body, "{") {
		t.Errorf("exit %d first line %q, want exit 0 and the server's body relayed", res.exit, first)
	}
	for _, want := range []string{"Run r1 did not take the retry budget", "Run r2 is retrying up to 5 times, not the 3 you asked for"} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr %q lacks %q", res.stderr, want)
		}
	}
}

func TestTailRetriesAcceptsTheBoundsOneAndTen(t *testing.T) {
	for _, value := range []string{"1", "10"} {
		t.Run(value, func(t *testing.T) {
			s := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: budgetWritten}}}

			res := runBinary(t, gitRepo(t), s.start(t).URL, "tail", "--run-id", "b1", "--retries="+value, "--json")

			if res.exit != 0 || len(s.bodies) != 1 || s.bodies[0] != `{"max_retries":`+value+`}` {
				t.Errorf("exit %d bodies %v, want exit 0 and one POST of %s", res.exit, s.bodies, value)
			}
		})
	}
}

func TestTailTimeoutCoversTheRetryBudgetPost(t *testing.T) {
	s := &budgetServer{postHang: 10 * time.Second, replies: []stubReply{{status: 200, body: budgetWritten}}, frames: []string{summaryPass}}
	srv := s.start(t)

	started := time.Now()
	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b1", "--retries", "3", "--timeout", "500ms", "--json")

	assertRefusal(t, res, 8, "timeout")
	if got := s.requests(); got != "POST retry_budget" || time.Since(started) > 5*time.Second {
		t.Errorf("requests %q after %s, want only the POST, cut off by --timeout", got, time.Since(started))
	}
}

func TestTailRetriesUnderWaitWritesAfterTheWaitingLinesAndBeforeTheStream(t *testing.T) {
	s := &budgetServer{lookups: []int{404}, frames: []string{frameStart, summaryPass}, replies: []stubReply{{status: 200, body: budgetWritten}}}
	srv := s.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--wait=30s", "--retries", "3", "--json")

	var kinds []string
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		var l struct {
			Event       string          `json:"event"`
			RetryBudget json.RawMessage `json:"retry_budget"`
		}
		_ = json.Unmarshal([]byte(line), &l)
		kinds = append(kinds, cmp.Or(l.Event, map[bool]string{true: "write"}[len(l.RetryBudget) > 0]))
	}
	want := "waiting,write,build_start,summary"
	if res.exit != 0 || strings.Join(kinds, ",") != want || s.requests() != "lookup,lookup,POST retry_budget,tail" {
		t.Errorf("exit %d lines %v requests %q, want %s", res.exit, kinds, s.requests(), want)
	}
}

func TestTailRetriesTextModeReportsTheBudgetInForce(t *testing.T) {
	cases := []struct{ name, runs, want string }{
		{"differs", `{"id":"r1","applied":false,"max_retries":null},{"id":"r2","applied":true,"max_retries":5}`, "Set the retry budget of build b1 to 5 (you asked for 3)."},
		{"none applied", `{"id":"r1","applied":false,"max_retries":null}`, "No run of build b1 took the retry budget (not live)."},
		{"as asked", `{"id":"r1","applied":true,"max_retries":3}`, "Set the retry budget of build b1 to 3."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"build_id":"b1","retry_budget":{"max_retries":3,"runs":[` + tc.runs + `]},"result":"updated","dry_run":false}`
			s := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: body}}}

			res := runBinary(t, gitRepo(t), s.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--format", "pretty")

			if first, _, _ := strings.Cut(res.stderr, "\n"); res.exit != 0 || first != tc.want {
				t.Errorf("exit %d stderr %q, want first line %q", res.exit, res.stderr, tc.want)
			}
		})
	}
}

func TestTailRetriesDryRunPreviewsAndDoesNotTail(t *testing.T) {
	preview := `{"build_id":"b1","retry_budget":{"max_retries":3,"runs":[{"id":"r1","applied":false,"max_retries":2,"requeued_test_ids":[]}]},"result":"updated","dry_run":true}`
	for _, mode := range []string{"--json", "--format=pretty"} {
		t.Run(mode, func(t *testing.T) {
			s := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: preview}}}

			res := runBinary(t, gitRepo(t), s.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--dry-run", mode)

			want := `{"v":1,` + strings.TrimPrefix(preview, "{") + "\n"
			if mode != "--json" {
				want = "Dry run: would set the retry budget of build b1 to 3 on 1 live run(s); nothing changed.\n"
			}
			if res.exit != 0 || res.stdout != want || res.stderr != "" || s.requests() != "POST retry_budget" || s.queries[0] != "dry_run=true" {
				t.Errorf("exit %d stdout %q stderr %q requests %q queries %v, want %q and only the dry POST", res.exit, res.stdout, res.stderr, s.requests(), s.queries, want)
			}
		})
	}
}

func TestTailRetriesDryRunDoesNotWaitForTheBuild(t *testing.T) {
	missing := &budgetServer{lookups: []int{404, 404, 404, 404}, replies: []stubReply{{status: 200, body: budgetWritten}}}
	res := runBinary(t, gitRepo(t), missing.start(t).URL, "tail", "--wait=5s", "--retries", "3", "--dry-run", "--json")
	assertRefusal(t, res, 3, "no_build")
	if got := missing.requests(); got != "lookup" {
		t.Errorf("requests = %q, want one lookup and no polling", got)
	}

	found := &budgetServer{replies: []stubReply{{status: 200, body: budgetWritten}}}
	res = runBinary(t, gitRepo(t), found.start(t).URL, "tail", "--wait=5s", "--retries", "3", "--dry-run", "--json")
	if got := found.requests(); res.exit != 0 || got != "lookup,POST retry_budget" {
		t.Errorf("exit %d requests %q, want exit 0 after one lookup and the dry POST", res.exit, got)
	}
}

func TestTailRetriesSendsTheOrgFromTheFlagOrTheEnvironment(t *testing.T) {
	flagged := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: budgetWritten}}}
	res := runBinary(t, gitRepo(t), flagged.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--org", "acme", "--json")
	if res.exit != 0 || len(flagged.queries) != 1 || flagged.queries[0] != "org=acme" {
		t.Errorf("exit %d queries %v stderr %q, want exit 0 and one POST with org=acme", res.exit, flagged.queries, res.stderr)
	}

	t.Setenv("SELECTIVE_ORG", "from-env")
	env := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: budgetWritten}}}
	runBinary(t, gitRepo(t), env.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--json")
	if len(env.queries) != 1 || env.queries[0] != "org=from-env" {
		t.Errorf("queries %v, want one POST with org=from-env", env.queries)
	}

	both := &budgetServer{frames: []string{summaryPass}, replies: []stubReply{{status: 200, body: budgetWritten}}}
	res = runBinary(t, gitRepo(t), both.start(t).URL, "tail", "--run-id", "b1", "--retries", "3", "--org", "acme", "--format", "pretty")
	first, _, _ := strings.Cut(res.stderr, "\n")
	if len(both.queries) != 1 || both.queries[0] != "org=acme" || first != "Set the retry budget of build b1 in org acme to 3." {
		t.Errorf("queries %v first line %q, want --org to beat SELECTIVE_ORG and the headline to name acme", both.queries, first)
	}

	plain := &budgetServer{frames: []string{summaryPass}}
	runBinary(t, gitRepo(t), plain.start(t).URL, "tail", "--run-id", "b1", "--json")
	if got := plain.requests(); got != "tail" {
		t.Errorf("requests = %q, want a plain tail to ignore SELECTIVE_ORG", got)
	}
	orgOnly := runBinary(t, gitRepo(t), plain.start(t).URL, "tail", "--run-id", "b1", "--org", "acme", "--json")
	assertRefusal(t, orgOnly, 5, "usage_error")
}
