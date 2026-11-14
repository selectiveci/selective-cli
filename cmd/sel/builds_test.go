package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const orgBuildsBody = `{"builds":[{"id":"b-1","status":"success","archived":false}],"next_cursor":null}`

type buildsServer struct {
	mu     sync.Mutex
	paths  []string
	status int
	body   string
}

func (s *buildsServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.RequestURI())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/orgs":
			_, _ = w.Write([]byte(`{"organizations":[{"slug":"acme"},{"slug":"globex"}]}`))
		case r.Header.Get("Authorization") == "Bearer tok" && (strings.HasSuffix(r.URL.Path, "/builds") || strings.HasPrefix(r.URL.Path, "/api/v1/builds/")):
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildsPrintsTheListDocumentWhenPiped(t *testing.T) {
	s := &buildsServer{status: 200, body: orgBuildsBody}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "builds", "--org", "acme", "--status", "success", "--branch", "main")

	if res.exit != 0 || res.stdout != `{"v":1,"builds":[{"id":"b-1","status":"success","archived":false}],"next_cursor":null}`+"\n" {
		t.Errorf("exit %d stdout %q stderr %q", res.exit, res.stdout, res.stderr)
	}
	if len(s.paths) != 1 || s.paths[0] != "/api/v1/orgs/acme/builds?branch=main&status=success" {
		t.Errorf("paths = %v", s.paths)
	}
}

func TestBuildsFlakyFlagSendsTheFlakyFilterAndNoStatus(t *testing.T) {
	s := &buildsServer{status: 200, body: orgBuildsBody}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "builds", "--org", "acme", "--flaky")

	if res.exit != 0 || len(s.paths) != 1 || s.paths[0] != "/api/v1/orgs/acme/builds?flaky=true" {
		t.Errorf("exit %d paths %v", res.exit, s.paths)
	}
}

func TestBuildPrintsTheResourceDocumentWhenPiped(t *testing.T) {
	s := &buildsServer{status: 200, body: `{"build":{"id":"b-1","completed_at":null,"runs":[]}}`}
	srv := s.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "build", "b-1", "--org", "acme")

	if res.exit != 0 || res.stdout != `{"v":1,"build":{"id":"b-1","completed_at":null,"runs":[]}}`+"\n" {
		t.Errorf("exit %d stdout %q stderr %q", res.exit, res.stdout, res.stderr)
	}
	if len(s.paths) != 1 || s.paths[0] != "/api/v1/builds/b-1?org=acme" {
		t.Errorf("paths = %v", s.paths)
	}
}

func TestBuildsAndBuildRefusalsExitByCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		exit   int
		code   string
		said   string
	}{
		{"status flaky is refused by the server", 422, `{"error":"invalid_request","message":"status must be one of failed, running, incomplete, cancelled, success"}`, []string{"builds", "--org", "acme", "--status", "flaky"}, 5, "invalid_request", ""},
		{"unknown org", 404, `{"error":"organization_not_found","message":"No such organization."}`, []string{"builds", "--org", "nope"}, 3, "organization_not_found", ""},
		{"builds forbidden", 403, `{"error":"forbidden","message":"Not allowed."}`, []string{"builds", "--org", "acme"}, 6, "forbidden", ""},
		{"builds bad token", 401, `{"error":"unauthorized","message":"Invalid token."}`, []string{"builds", "--org", "acme"}, 4, "unauthorized", ""},
		{"unresolved org", 200, `{}`, []string{"builds"}, 5, "usage_error", "you belong to several organizations; pass --org (one of: acme, globex)"},
		{"unknown build", 404, `{"error":"not_found","message":"No build with this id."}`, []string{"build", "nope"}, 3, "not_found", ""},
		{"ambiguous build", 409, `{"error":"ambiguous_build","message":"This build id exists in more than one organization. Pass org."}`, []string{"build", "b-1"}, 5, "ambiguous_build", ""},
		{"missing build argument", 200, `{}`, []string{"build"}, 5, "usage_error", "accepts 1 arg(s), received 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &buildsServer{status: tc.status, body: tc.body}
			srv := s.start(t)

			res := runBinary(t, t.TempDir(), srv.URL, append(tc.args, "--json")...)

			assertRefusal(t, res, tc.exit, tc.code)
			if !strings.Contains(res.stdout, tc.said) {
				t.Errorf("stdout %q lacks %q", res.stdout, tc.said)
			}
		})
	}
}

func TestBuildsRejectsALimitBelowOne(t *testing.T) {
	s := &buildsServer{status: 200, body: orgBuildsBody}
	srv := s.start(t)

	for _, limit := range []string{"0", "-1"} {
		t.Run(limit, func(t *testing.T) {
			res := runBinary(t, t.TempDir(), srv.URL, "builds", "--org", "acme", "--limit="+limit, "--json")

			assertRefusal(t, res, 5, "usage_error")
			if !strings.Contains(res.stdout, "--limit must be at least 1") || len(s.paths) != 0 {
				t.Errorf("stdout %q paths %v, want the limit refusal and no request", res.stdout, s.paths)
			}
		})
	}
}

func TestBuildsAndBuildListTheirExitCodesInTheSchema(t *testing.T) {
	for _, name := range []string{"builds", "build"} {
		code, stdout, _ := execute("help", name, "--json")

		if code != 0 || !strings.Contains(stdout, `"exit_codes":[0,3,4,5,6]`) {
			t.Errorf("help %s: exit %d stdout %s", name, code, stdout)
		}
	}
}
