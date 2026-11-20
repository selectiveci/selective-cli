package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nhooyr.io/websocket"
)

type rerunWaitServer struct {
	mu        sync.Mutex
	reply     stubReply
	frames    []string
	hold      bool
	requests  []string
	tailQuery string
}

func (s *rerunWaitServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/builds/b1/rerun":
			w.WriteHeader(s.reply.status)
			_, _ = w.Write([]byte(s.reply.body))
		case "/api/v1/tail/websocket":
			s.mu.Lock()
			s.tailQuery = r.URL.RawQuery
			s.mu.Unlock()
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
			if s.hold {
				<-ctx.Done()
				return
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *rerunWaitServer) seen() ([]string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...), s.tailQuery
}

func rerunWriteLine(body string) string {
	return `{"v":1,` + strings.TrimPrefix(body, "{") + "\n"
}

func TestRerunWaitPostsThenFollowsTheTailWithoutReplayAndStreamsToTheSummary(t *testing.T) {
	s := &rerunWaitServer{reply: stubReply{status: 201, body: rerunCreated}, frames: []string{frameStart, frameFail, summaryFail}}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "rerun", "--failed", "b1", "--wait", "--json")

	want := rerunWriteLine(rerunCreated) + frameStart + "\n" + frameFail + "\n" + summaryFail + "\n"
	if res.exit != 1 || res.stdout != want {
		t.Errorf("exit %d stdout %q, want exit 1 and %q (stderr: %s)", res.exit, res.stdout, want, res.stderr)
	}
	requests, query := s.seen()
	if len(requests) != 2 || requests[0] != "POST /api/v1/builds/b1/rerun" || requests[1] != "GET /api/v1/tail/websocket" {
		t.Errorf("requests = %v, want the rerun POST and then the tail attach", requests)
	}
	if query != "build_id=b1&v=1" {
		t.Errorf("tail query = %q, want build_id=b1&v=1 with no replay", query)
	}
}

func TestRerunWaitExitsWithTheOutcomeOfTheRerun(t *testing.T) {
	cases := map[string]struct {
		summary string
		exit    int
	}{
		"every test passed":             {summaryPass, 0},
		"failures remain":               {summaryFail, 1},
		"every failure passed on retry": {summaryFlaky, 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &rerunWaitServer{reply: stubReply{status: 201, body: rerunCreated}, frames: []string{tc.summary}}
			srv := s.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, "rerun", "--failed", "b1", "--wait=1m", "--json")

			if res.exit != tc.exit || !strings.HasSuffix(res.stdout, tc.summary+"\n") {
				t.Errorf("exit %d stdout %q, want exit %d ending in %s", res.exit, res.stdout, tc.exit, tc.summary)
			}
		})
	}
}

func TestRerunWaitThatExpiresExitsEightWithTheTimeoutErrorLast(t *testing.T) {
	cases := map[string]struct {
		args []string
		flag string
	}{
		"--wait is the limit":            {[]string{"--wait=1s"}, "--wait 1s"},
		"--timeout is the tighter limit": {[]string{"--wait=1m", "--timeout=1s"}, "--timeout 1s"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &rerunWaitServer{reply: stubReply{status: 201, body: rerunCreated}, frames: []string{frameStart}, hold: true}
			srv := s.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append([]string{"rerun", "--failed", "b1", "--json"}, tc.args...)...)

			lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
			last := lines[len(lines)-1]
			if res.exit != 8 || !strings.HasPrefix(last, `{"v":1,"error":"timeout","message":"`+tc.flag+` expired`) {
				t.Errorf("exit %d last line %q, want exit 8 and a timeout error naming %s", res.exit, last, tc.flag)
			}
			if lines[0]+"\n" != rerunWriteLine(rerunCreated) {
				t.Errorf("first line %q, want the write response", lines[0])
			}
		})
	}
}

func TestRerunWaitWithDryRunQueuesNothingAndDoesNotAttach(t *testing.T) {
	dry := strings.Replace(rerunCreated, `"dry_run":false`, `"dry_run":true`, 1)
	s := &rerunWaitServer{reply: stubReply{status: 200, body: dry}, frames: []string{summaryFail}}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "rerun", "--failed", "b1", "--dry-run", "--wait", "--json")

	requests, _ := s.seen()
	if res.exit != 0 || res.stdout != rerunWriteLine(dry) || len(requests) != 1 {
		t.Errorf("exit %d stdout %q requests %v, want exit 0, the dry run object and one request", res.exit, res.stdout, requests)
	}
}

func TestRerunWaitDoesNotAttachWhenTheRerunIsRefused(t *testing.T) {
	s := &rerunWaitServer{reply: stubReply{status: 409, body: `{"error":"run_not_running","message":"The build has finished."}`}, frames: []string{summaryPass}}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "rerun", "--failed", "b1", "--wait", "--json")

	requests, _ := s.seen()
	assertRefusal(t, res, 5, "run_not_running")
	if len(requests) != 1 {
		t.Errorf("requests = %v, want only the refused POST", requests)
	}
}

func TestRerunWaitRefusesValuesItCannotHonourBeforeSendingAnything(t *testing.T) {
	cases := map[string][]string{
		"--wait of zero":               {"--wait=0s"},
		"--timeout of zero":            {"--wait", "--timeout=0s"},
		"--timeout without --wait":     {"--timeout=5s"},
		"--wait with a separate value": {"--wait", "5s"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			s := &rerunWaitServer{reply: stubReply{status: 201, body: rerunCreated}, frames: []string{summaryPass}}
			srv := s.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append([]string{"rerun", "--failed", "b1", "--json"}, args...)...)

			requests, _ := s.seen()
			assertRefusal(t, res, 5, "usage_error")
			if len(requests) != 0 {
				t.Errorf("requests = %v, want none", requests)
			}
		})
	}
}
