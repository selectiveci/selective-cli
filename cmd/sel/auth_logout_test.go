package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/config"
)

type revokeServer struct {
	mu       sync.Mutex
	requests []string
	status   int
}

func (s *revokeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		if s.status >= 400 {
			_, _ = w.Write([]byte(`{"error":"server_error","message":"boom"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *revokeServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func storedHosts(t *testing.T, dir string) map[string]*config.Host {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Hosts
}

func TestAuthLogoutWritesAWriteObjectToStdout(t *testing.T) {
	cases := []struct {
		name         string
		revokeStatus int
		loggedIn     bool
		args         []string
		want         string
		requests     int
		stillStored  bool
	}{
		{"revoked and forgotten", 204, true, nil, `"revoked":true},"result":"deleted","dry_run":false}`, 1, false},
		{"server refuses the revoke", 500, true, nil, `"revoked":false},"result":"deleted","dry_run":false}`, 1, false},
		{"token already revoked elsewhere", 401, true, nil, `"revoked":true},"result":"deleted","dry_run":false}`, 1, false},
		{"not logged in", 204, false, nil, `"revoked":false},"result":"unchanged","dry_run":false}`, 0, false},
		{"dry run while logged in", 204, true, []string{"--dry-run"}, `"revoked":false},"result":"deleted","dry_run":true}`, 0, true},
		{"dry run while not logged in", 204, false, []string{"--dry-run"}, `"revoked":false},"result":"unchanged","dry_run":true}`, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &revokeServer{status: tc.revokeStatus}
			srv := stub.start(t)
			dir := t.TempDir()
			if tc.loggedIn {
				dir = storeInConfigFile(t, srv.URL)
			}

			res := selRun{host: srv.URL, configDir: dir}.run(t, append([]string{"auth", "logout"}, tc.args...)...)

			want := `{"v":1,"auth":{"host":"` + srv.URL + `",` + tc.want + "\n"
			if res.exit != 0 || res.stdout != want {
				t.Errorf("exit %d stdout %q, want exit 0 and %q", res.exit, res.stdout, want)
			}
			if got := stub.seen(); len(got) != tc.requests || (tc.requests > 0 && got[0] != "DELETE /api/v1/tokens/current") {
				t.Errorf("server saw %v, want %d revoke request", got, tc.requests)
			}
			if tc.loggedIn && (storedHosts(t, dir)[srv.URL] != nil) != tc.stillStored {
				t.Errorf("credential still stored = %v, want %v", !tc.stillStored, tc.stillStored)
			}
		})
	}
}

func TestAuthLogoutWithAnEnvTokenStaysAUsageErrorInADryRun(t *testing.T) {
	stub := &revokeServer{status: 204}
	srv := stub.start(t)

	res := selRun{host: srv.URL, token: storedToken}.run(t, "auth", "logout", "--dry-run")

	if res.exit != 5 || !strings.HasPrefix(res.stdout, `{"v":1,"error":"usage_error"`) || len(stub.seen()) != 0 {
		t.Errorf("exit %d stdout %q requests %v", res.exit, res.stdout, stub.seen())
	}
}

func TestHelpMarksAuthLogoutAsAWriteWithADryRunFlag(t *testing.T) {
	_, stdout, _ := execute("help", "auth", "logout", "--json")

	var doc struct {
		Command struct {
			Mutates bool `json:"mutates"`
			Flags   []struct {
				Name string `json:"name"`
			} `json:"flags"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	flags := []string{}
	for _, f := range doc.Command.Flags {
		flags = append(flags, f.Name)
	}
	if !doc.Command.Mutates || !strings.Contains(strings.Join(flags, " "), "dry-run") {
		t.Errorf("mutates %v, flags %v", doc.Command.Mutates, flags)
	}
}
