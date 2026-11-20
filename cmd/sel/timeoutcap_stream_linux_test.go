package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func endlessBuildStream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := conn.CloseRead(context.Background())
		for i := 0; ; i++ {
			line := fmt.Sprintf(`{"v":1,"event":"test_pass","n":%d,"pad":"%s"}`, i, strings.Repeat("x", 200))
			if conn.Write(ctx, websocket.MessageText, []byte(line)) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func configThatUnblocksAfter(t *testing.T, stall time.Duration) string {
	t.Helper()
	dir := stalledConfigDir(t)
	path := filepath.Join(dir, "config.json")
	go func() {
		time.Sleep(stall)
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			_, _ = f.WriteString("{}")
			f.Close()
		}
	}()
	return dir
}

func TestTimeoutObjectIsTheLastStdoutLineWhenSetupOutlastsTheCapMidStream(t *testing.T) {
	srv := endlessBuildStream(t)
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint("run", i), func(t *testing.T) {
			t.Parallel()
			env := []string{"SELECTIVE_HOST=" + srv.URL, "SELECTIVE_NO_UPDATE_NOTIFIER=1"}
			res, _ := runBinaryWithin(t, 6*time.Second, t.TempDir(), configThatUnblocksAfter(t, 1500*time.Millisecond), env, "tail", "--run-id", "r1", "--timeout=1s", "--json")

			lines := strings.Split(strings.TrimSuffix(res.stdout, "\n"), "\n")
			var last struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.Error != "timeout" || res.exit != 8 {
				t.Errorf("exit %d, %d lines, last line %.100q; want exit 8 with the timeout object last", res.exit, len(lines), lines[len(lines)-1])
			}
		})
	}
}

func TestTimeoutExitsEightEvenWhenTheUpdateCheckStateReadIsStalledToo(t *testing.T) {
	dir := stalledConfigDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "update-check.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"SELECTIVE_NO_UPDATE_NOTIFIER=", "CI=", "GITHUB_ACTIONS=", "BUILD_NUMBER=", "RUN_ID=", "CODESPACES="}

	res, took := runBinaryWithin(t, 6*time.Second, t.TempDir(), dir, env, "approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90", "--timeout=1s", "--json")

	if res.exit != 8 || took > 3*time.Second {
		t.Errorf("exit %d after %s, want exit 8 within 3s with the notifier on", res.exit, took)
	}
}

func TestSelTestTimeoutCapsASetupReadThatNeverReturns(t *testing.T) {
	res, took := runBinaryWithin(t, 6*time.Second, pytestProject(t), stalledConfigDir(t), nil, "test", "--org", "acme", "--timeout=1s", "--json")

	lines := strings.Split(strings.TrimSuffix(res.stdout, "\n"), "\n")
	if res.exit != 8 || took > 3*time.Second || !strings.Contains(lines[len(lines)-1], `"error":"timeout"`) {
		t.Errorf("exit %d after %s, stdout %q; want exit 8 within 3s ending in the timeout object", res.exit, took, res.stdout)
	}
}
