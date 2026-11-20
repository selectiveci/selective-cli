package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nhooyr.io/websocket"
)

type budgetServer struct {
	mu      sync.Mutex
	log     []string
	keys    []string
	bodies  []string
	replies []stubReply
	frames  []string
}

const budgetWritten = `{"build_id":"b1","retry_budget":{"max_retries":3,"runs":[{"id":"r1","requeued_test_ids":[]}]},"result":"updated","dry_run":false}`

func (s *budgetServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/builds/lookup":
			s.log = append(s.log, "lookup")
			_, _ = w.Write([]byte(`{"build_id":"b1","status":"running"}`))
		case "/api/v1/builds/b1/retry_budget":
			body, _ := io.ReadAll(r.Body)
			s.log = append(s.log, r.Method+" retry_budget")
			s.keys = append(s.keys, r.Header.Get("Idempotency-Key"))
			s.bodies = append(s.bodies, string(body))
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
	assertRefusal(t, keyed, 5, "usage_error")
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
