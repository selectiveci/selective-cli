package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type trendsServer struct {
	mu     sync.Mutex
	paths  []string
	status int
	body   string
}

func (s *trendsServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/orgs":
			_, _ = w.Write([]byte(`{"organizations":[{"slug":"acme"},{"slug":"globex"}]}`))
		case r.Header.Get("Authorization") == "Bearer tok" && strings.HasSuffix(r.URL.Path, "/trends"):
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTrendsPrintsTheDocumentWhenPiped(t *testing.T) {
	s := &trendsServer{status: 200, body: `{"range":"day","suite_id":null,"summary":{"pass_rate":null}}`}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "trends", "--org", "acme", "--range", "day", "--section", "summary", "--limit", "5")

	if res.exit != 0 || res.stdout != `{"v":1,"range":"day","suite_id":null,"summary":{"pass_rate":null}}`+"\n" {
		t.Errorf("exit %d stdout %q stderr %q", res.exit, res.stdout, res.stderr)
	}
	if len(s.paths) != 1 || s.paths[0] != "/api/v1/orgs/acme/trends?limit=5&range=day&section=summary" {
		t.Errorf("paths = %v", s.paths)
	}
}

func TestTrendsRefusalsExitByCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		exit   int
		code   string
		said   string
	}{
		{"repo-scoped token without a suite", 403, `{"error":"forbidden","message":"This token is limited to specific repositories. Pass --suite to read one suite's trends."}`, []string{"--org", "acme"}, 6, "forbidden", "Pass --suite"},
		{"invisible suite", 404, `{"error":"suite_not_found","message":"No such suite in this organization."}`, []string{"--org", "acme", "--suite", "nope"}, 3, "suite_not_found", ""},
		{"retired section name", 422, `{"error":"invalid_request","message":"section must be one of: summary, outcomes."}`, []string{"--org", "acme", "--section", "pass_rate"}, 5, "invalid_request", ""},
		{"statement timeout", 503, `{"error":"server_error","message":"Trends took too long to compute. Retry."}`, []string{"--org", "acme"}, 4, "server_error", ""},
		{"unresolved org", 200, `{}`, nil, 5, "usage_error", "you belong to several organizations; pass --org (one of: acme, globex)"},
		{"limit below one", 200, `{}`, []string{"--org", "acme", "--limit", "0"}, 5, "usage_error", "--limit must be at least 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &trendsServer{status: tc.status, body: tc.body}
			srv := s.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append(append([]string{"trends"}, tc.args...), "--json")...)

			assertRefusal(t, res, tc.exit, tc.code)
			if !strings.Contains(res.stdout, tc.said) {
				t.Errorf("stdout %q lacks %q", res.stdout, tc.said)
			}
		})
	}
}

func TestTrendsListsItsExitCodesInTheSchema(t *testing.T) {
	code, stdout, _ := execute("help", "trends", "--json")

	if code != 0 || !strings.Contains(stdout, `"exit_codes":[0,3,4,5,6]`) {
		t.Errorf("exit %d stdout %s", code, stdout)
	}
}
