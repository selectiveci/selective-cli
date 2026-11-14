package tail

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func capture(t *testing.T) (out, errOut *bytes.Buffer) {
	t.Helper()
	oldOut, oldErr := outWriter, errWriter
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	outWriter, errWriter = out, errOut
	t.Cleanup(func() { outWriter, errWriter = oldOut, oldErr })
	return out, errOut
}

func recordingTailServer(t *testing.T, frames []string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		query = r.URL.RawQuery
		mu.Unlock()
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for _, f := range frames {
			if c.Write(context.Background(), websocket.MessageText, []byte(f)) != nil {
				return
			}
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return query
	}
}

const (
	replayedFail     = `{"v":1,"event":"test_fail","id":"f1","node":"n1","file":"a_test.rb","line":3,"error":"boom","replayed":true}`
	replayTruncated  = `{"v":1,"event":"replay_truncated","shown":500,"total":900}`
	summaryOneFailed = `{"v":1,"event":"summary","passed":4,"failed":1,"flaky":0,"ms":10}`
	summaryOneFlaky  = `{"v":1,"event":"summary","passed":5,"failed":0,"flaky":1,"ms":10}`
)

func TestStreamAsksTheServerToReplayFailuresAtVersionOne(t *testing.T) {
	capture(t)
	srv, query := recordingTailServer(t, []string{summaryOneFailed})

	_, _ = stream(context.Background(), srv.URL, "tok", "b", "ndjson")

	if want := "build_id=b&replay=1&v=1"; query() != want {
		t.Errorf("query = %q, want %q", query(), want)
	}
}

func TestStreamPassesReplayedFramesThroughAndExitsFromTheSummary(t *testing.T) {
	cases := []struct {
		name   string
		frames []string
		want   int
	}{
		{"a late join to a failed build", []string{buildStart, replayedFail, summaryOneFailed}, 1},
		{"a replayed failure that passed on retry", []string{buildStart, replayedFail, summaryOneFlaky}, 2},
		{"a truncated replay", []string{buildStart, replayedFail, replayTruncated, summaryOneFailed}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := capture(t)
			srv, _ := recordingTailServer(t, tc.frames)

			code, err := stream(context.Background(), srv.URL, "tok", "b", "ndjson")

			if want := strings.Join(tc.frames, "\n") + "\n"; code != tc.want || err != nil || out.String() != want {
				t.Errorf("exit %d err %v stdout %q, want exit %d and %q", code, err, out.String(), tc.want, want)
			}
		})
	}
}

func TestStreamWritesAHeartbeatLineForEveryQuietInterval(t *testing.T) {
	out, errOut := capture(t)
	heartbeatInterval = time.Second
	t.Cleanup(func() { heartbeatInterval = 30 * time.Second })
	srv := quietTailServer(t, true, 2500*time.Millisecond, summaryOneFailed)

	code, err := stream(context.Background(), srv.URL, "tok", "b", "ndjson")

	want := buildStart + "\n" +
		`{"v":1,"event":"heartbeat","elapsed_s":1}` + "\n" +
		`{"v":1,"event":"heartbeat","elapsed_s":2}` + "\n" +
		summaryOneFailed + "\n"
	if code != 1 || err != nil || out.String() != want || errOut.Len() != 0 {
		t.Errorf("exit %d err %v stdout %q stderr %q, want exit 1, %q and no stderr", code, err, out.String(), errOut.String(), want)
	}
}

func TestStreamKeepsHeartbeatsOffStdoutInPrettyMode(t *testing.T) {
	out, errOut := capture(t)
	heartbeatInterval = time.Second
	t.Cleanup(func() { heartbeatInterval = 30 * time.Second })
	srv := quietTailServer(t, true, 1500*time.Millisecond, summaryOneFailed)

	code, err := stream(context.Background(), srv.URL, "tok", "b", "pretty")

	if code != 1 || err != nil || strings.Contains(out.String(), "heartbeat") || strings.Count(errOut.String(), "still running") != 1 {
		t.Errorf("exit %d err %v stdout %q stderr %q, want exit 1, one stderr note and no heartbeat on stdout", code, err, out.String(), errOut.String())
	}
}

func TestStreamStaysSilentWhileFramesKeepArriving(t *testing.T) {
	out, _ := capture(t)
	heartbeatInterval = time.Second
	t.Cleanup(func() { heartbeatInterval = 30 * time.Second })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for _, f := range []string{buildStart, testFail, testFail, testFail, summaryOneFailed} {
			if c.Write(context.Background(), websocket.MessageText, []byte(f)) != nil {
				return
			}
			time.Sleep(600 * time.Millisecond)
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)

	code, err := stream(context.Background(), srv.URL, "tok", "b", "ndjson")

	if code != 1 || err != nil || strings.Contains(out.String(), "heartbeat") {
		t.Errorf("exit %d err %v stdout %q, want exit 1 and no heartbeat while a frame arrives every 600ms", code, err, out.String())
	}
}
