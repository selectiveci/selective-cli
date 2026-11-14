package tail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

const (
	buildStart = `{"event":"build_start","build_id":"b","commit":"abc1234"}`
	testFail   = `{"event":"test_fail","node":"n1","file":"a_test.rb","line":3,"error":"boom"}`
	progress   = `{"event":"progress","completed":1,"total":10,"percent":10,"failed":0}`
)

func fakeTailServer(t *testing.T, frames []string, dropWithoutClose bool) (*httptest.Server, *int32) {
	t.Helper()
	var connects int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tail/websocket" || r.URL.Query().Get("build_id") != "b" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Selective-Token") != "tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&connects, 1)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		for _, f := range frames {
			if err := c.Write(context.Background(), websocket.MessageText, []byte(f)); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
		if dropWithoutClose {
			_ = c.CloseNow()
			return
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return srv, &connects
}

func TestStreamExitCodes(t *testing.T) {
	cases := []struct {
		name    string
		frames  []string
		drop    bool
		want    int
		wantErr bool
	}{
		{
			name:   "all tests pass",
			frames: []string{buildStart, progress, `{"event":"summary","passed":5,"failed":0,"flaky":0,"ms":10}`},
			want:   0,
		},
		{
			name:   "a failure seen live",
			frames: []string{buildStart, testFail, `{"event":"summary","passed":4,"failed":1,"flaky":0,"ms":10}`},
			want:   1,
		},
		{
			name:   "joining a build that already finished with failures",
			frames: []string{buildStart, `{"event":"summary","passed":2,"failed":3,"flaky":0,"ms":10}`},
			want:   1,
		},
		{
			name:   "a real failure alongside a test that passed on retry",
			frames: []string{buildStart, testFail, `{"event":"summary","passed":4,"failed":1,"flaky":1,"ms":10}`},
			want:   1,
		},
		{
			name:   "joining a build where a test passed on retry",
			frames: []string{buildStart, `{"event":"summary","passed":5,"failed":0,"flaky":1,"ms":10}`},
			want:   2,
		},
		{
			name:   "a failure seen live that passed on retry, from a server that reports no flaky count",
			frames: []string{buildStart, testFail, `{"event":"summary","passed":5,"failed":0,"flaky":0,"ms":10}`},
			want:   2,
		},
		{
			name:    "connection dropped before the summary",
			frames:  []string{buildStart, progress},
			drop:    true,
			want:    4,
			wantErr: true,
		},
		{
			name:    "server closed cleanly without a summary",
			frames:  []string{buildStart, testFail},
			want:    4,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, connects := fakeTailServer(t, tc.frames, tc.drop)

			code, err := stream(context.Background(), srv.URL, "tok", "b", "ndjson")

			if got := atomic.LoadInt32(connects); got != 1 {
				t.Fatalf("server saw %d connections, want 1", got)
			}
			if code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "lost the connection") {
				t.Errorf("err = %q, want a lost-connection error", err)
			}
		})
	}
}

func quietTailServer(t *testing.T, answerPings bool, after time.Duration, last string) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := context.Background()
		if err := c.Write(ctx, websocket.MessageText, []byte(buildStart)); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		if !answerPings {
			<-release
			return
		}
		ctx = c.CloseRead(ctx)
		select {
		case <-time.After(after):
		case <-ctx.Done():
			return
		}
		if err := c.Write(context.Background(), websocket.MessageText, []byte(last)); err != nil {
			t.Errorf("write: %v", err)
			return
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv
}

func shortPings(t *testing.T) {
	t.Helper()
	old := pingInterval
	pingInterval = 50 * time.Millisecond
	t.Cleanup(func() { pingInterval = old })
}

type streamResult struct {
	code int
	err  error
}

func streamWithin(t *testing.T, srv *httptest.Server, limit time.Duration) streamResult {
	t.Helper()
	done := make(chan streamResult, 1)
	go func() {
		code, err := stream(context.Background(), srv.URL, "tok", "b", "ndjson")
		done <- streamResult{code, err}
	}()
	select {
	case res := <-done:
		return res
	case <-time.After(limit):
		t.Fatalf("tail still running after %s", limit)
		return streamResult{}
	}
}

func TestStreamGivesUpOnAnUnresponsiveServer(t *testing.T) {
	shortPings(t)
	srv := quietTailServer(t, false, 0, "")

	res := streamWithin(t, srv, 2*time.Second)

	if res.code != 4 {
		t.Errorf("exit code = %d, want 4", res.code)
	}
	if res.err == nil || !strings.Contains(res.err.Error(), "no response from Selective") {
		t.Errorf("err = %v, want a no-response error", res.err)
	}
}

func TestStreamStaysConnectedToAQuietServer(t *testing.T) {
	shortPings(t)
	summary := `{"event":"summary","passed":5,"failed":0,"flaky":0,"ms":10}`
	srv := quietTailServer(t, true, 8*pingInterval, summary)

	res := streamWithin(t, srv, 5*time.Second)

	if res.code != 0 || res.err != nil {
		t.Errorf("got exit %d err %v, want exit 0", res.code, res.err)
	}
}

func TestStreamRejectedToken(t *testing.T) {
	srv, connects := fakeTailServer(t, nil, false)

	code, err := stream(context.Background(), srv.URL, "wrong", "b", "ndjson")

	if got := atomic.LoadInt32(connects); got != 0 {
		t.Fatalf("server accepted %d connections with a bad token", got)
	}
	if code != 4 || err == nil {
		t.Errorf("got exit %d err %v, want exit 4 with an error", code, err)
	}
}
