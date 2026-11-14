package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nhooyr.io/websocket"
)

type waitServer struct {
	missingFor int32
	frames     []string
	hold       bool
	lookups    atomic.Int32
}

func (s *waitServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/builds/lookup":
			if s.lookups.Add(1) <= s.missingFor {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"no_build","message":"No build for this repository and commit."}`))
				return
			}
			_, _ = w.Write([]byte(`{"build_id":"b","status":"running","run_count":1,"suite_count":1}`))
		case "/api/v1/tail/websocket":
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

func TestTailWaitFindsABuildThatAppearsLaterAndExitsWithItsOutcome(t *testing.T) {
	s := &waitServer{missingFor: 1, frames: []string{frameStart, frameFail, summaryFail}}
	srv := s.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--wait=1m", "--format", "ndjson")

	wantStdout := `{"v":1,"event":"waiting","elapsed_s":0,"for":"build"}` + "\n" + frameStart + "\n" + frameFail + "\n" + summaryFail + "\n"
	if res.exit != 1 || res.stdout != wantStdout {
		t.Errorf("exit = %d stdout = %q, want exit 1 and %q (stderr: %s)", res.exit, res.stdout, wantStdout, res.stderr)
	}
	if got := s.lookups.Load(); got != 2 {
		t.Errorf("lookups = %d, want 2", got)
	}
}

func TestTailWaitThatExpiresExitsThreeWithNoBuildOnStdout(t *testing.T) {
	s := &waitServer{missingFor: 1000}
	srv := s.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--wait=1s", "--json")

	if res.exit != 3 {
		t.Errorf("exit = %d, want 3 (stderr: %s)", res.exit, res.stderr)
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], `{"v":1,"event":"waiting","elapsed_s":0,`) || !strings.HasPrefix(lines[1], `{"v":1,"error":"no_build","message":"no build for repo \"acme/widget\"`) {
		t.Errorf("stdout = %q, want a waiting line then the no_build error object", res.stdout)
	}
}

func TestTailWithoutWaitDoesNotPoll(t *testing.T) {
	s := &waitServer{missingFor: 1000}
	srv := s.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--json")

	if got := s.lookups.Load(); res.exit != 3 || got != 1 {
		t.Errorf("exit = %d lookups = %d, want exit 3 after one lookup", res.exit, got)
	}
}

func TestTailTimeoutOnAStreamThatNeverFinishesExitsEight(t *testing.T) {
	srv := (&waitServer{frames: []string{frameStart}, hold: true}).start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--timeout=1s", "--json")

	if res.exit != 8 || !strings.Contains(res.stdout, `{"v":1,"error":"timeout","message":"--timeout 1s expired`) {
		t.Errorf("exit = %d stdout = %q, want exit 8 with a timeout error object", res.exit, res.stdout)
	}
	if !strings.HasPrefix(res.stdout, frameStart+"\n") {
		t.Errorf("stdout = %q, want the frames already received to be kept", res.stdout)
	}
}

func TestTailTimeoutDuringTheWaitExitsEightNotThree(t *testing.T) {
	srv := (&waitServer{missingFor: 1000}).start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--wait", "--timeout=1s", "--json")

	if res.exit != 8 || !strings.Contains(res.stdout, `"error":"timeout"`) {
		t.Errorf("exit = %d stdout = %q, want exit 8 with a timeout error object (a bare --wait must also parse)", res.exit, res.stdout)
	}
}

func TestTailRefusesWaitAndTimeoutValuesItCannotHonour(t *testing.T) {
	srv := (&waitServer{missingFor: 1000}).start(t)
	cases := map[string][]string{
		"--wait with a separate value":      {"--wait", "5s"},
		"--wait of zero":                    {"--wait=0s"},
		"negative --timeout":                {"--timeout=-1s"},
		"--wait with something not a time":  {"--wait=soon"},
		"--timeout with something not time": {"--timeout=soon"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			res := runBinary(t, gitRepo(t), srv.URL, append([]string{"tail", "--json"}, args...)...)

			assertRefusal(t, res, 5, "usage_error")
		})
	}
}
