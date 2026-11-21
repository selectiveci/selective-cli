package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func longConfigDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), strings.Repeat(strings.Repeat("p", 200)+"/", 19))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stalledServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func fillPipeLeavingOneSlot(t *testing.T, r, w *os.File) (remaining int) {
	t.Helper()
	line := []byte(`{"p":"` + strings.Repeat("x", 4087) + "\"}\n")
	total := 0
	for {
		_ = w.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := w.Write(line)
		total += n
		if err != nil {
			break
		}
	}
	_ = w.SetWriteDeadline(time.Time{})
	if total%len(line) != 0 || total < 2*len(line) {
		t.Fatalf("the pipe filled to %d bytes, not whole %d-byte slots", total, len(line))
	}
	if _, err := io.ReadFull(r, make([]byte, len(line))); err != nil {
		t.Fatal(err)
	}
	return total - len(line)
}

func TestTimeoutLeavesNoCutLineWhenTheErrorObjectOutgrowsTheFreeSpaceInThePipe(t *testing.T) {
	longConfig := []string{"SELECTIVE_CONFIG_DIR=" + longConfigDir(t)}
	cases := map[string]struct {
		host, args string
		env        []string
		shared     bool
	}{
		"doctor timeout, stderr apart": {stalledServer(t), "doctor", longConfig, false},
		"doctor timeout, 2>&1":         {stalledServer(t), "doctor", longConfig, true},
		"network_error":                {"http://" + strings.Repeat("h", 5000) + ".invalid", "approvals show 0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90", nil, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			prefilled := fillPipeLeavingOneSlot(t, r, w)
			cmd, stderr := startWithStdout(t, t.TempDir(), c.host, c.env, w, c.shared, append(strings.Fields(c.args), "--json", "--timeout=1s")...)
			_ = w.Close()
			exit, took := waitForExit(t, cmd, 5*time.Second)
			dump, _ := io.ReadAll(r)

			if len(dump) <= prefilled {
				t.Fatalf("exit %d after %s with %d bytes in the pipe, no more than the %d prefilled: nothing was written, so this proves nothing", exit, took, len(dump), prefilled)
			}
			lines := strings.Split(string(dump[prefilled:]), "\n")
			for i, line := range lines[:len(lines)-1] {
				if !json.Valid([]byte(line)) {
					t.Fatalf("stdout line %d is not complete JSON (%d bytes): %.60q", i, len(line), line)
				}
			}
			if len(lines) < 2 || len(lines[0]) >= 512 {
				t.Fatalf("stdout does not start with a complete object of at most 512 bytes: %.80q", lines[0])
			}
			t.Logf("exit %d after %s, %d bytes after the last newline", exit, took, len(lines[len(lines)-1]))
			if tail := lines[len(lines)-1]; tail != "" && !(c.shared && strings.HasPrefix(tail, `{"v":1,"doctor":`)) {
				t.Errorf("stdout ends on an unterminated line of %d bytes: %.60q", len(tail), tail)
			}
			if c.args == "doctor" && (exit != 8 || took > hardBound) || strings.Contains(stderr.String(), `{"v":1,"doctor":`) != (c.args == "doctor" && !c.shared) {
				t.Errorf("exit %d after %s, stderr %.60q; want exit 8 within %s and doctor's document on stderr unless it shares the pipe", exit, took, stderr.String(), hardBound)
			}
		})
	}
}
