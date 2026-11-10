package tail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
			name:   "every failure is flaky",
			frames: []string{buildStart, testFail, `{"event":"summary","passed":4,"failed":1,"flaky":1,"ms":10}`},
			want:   2,
		},
		{
			name:   "a failure that passed on retry",
			frames: []string{buildStart, testFail, `{"event":"summary","passed":5,"failed":0,"flaky":0,"ms":10}`},
			want:   0,
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

			code, err := stream(srv.URL, "tok", "b", "ndjson")

			if got := atomic.LoadInt32(connects); got != 1 {
				t.Fatalf("server saw %d connections, want 1", got)
			}
			if code != tc.want {
				t.Errorf("exit code = %d, want %d", code, tc.want)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestStreamRejectedToken(t *testing.T) {
	srv, connects := fakeTailServer(t, nil, false)

	code, err := stream(srv.URL, "wrong", "b", "ndjson")

	if got := atomic.LoadInt32(connects); got != 0 {
		t.Fatalf("server accepted %d connections with a bad token", got)
	}
	if code != 4 || err == nil {
		t.Errorf("got exit %d err %v, want exit 4 with an error", code, err)
	}
}
