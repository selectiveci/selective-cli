package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	flakeID   = "11111111-1111-4111-8111-111111111111"
	flakeRows = `{"id":"` + flakeID + `","status":"flaking","test":{"id":"t","name":"checkout","path":"a_spec.rb"},"suite":{"id":"s","name":"web"},"last_seen_at":"2026-11-12T16:30:00Z","occurrence_count":2,"is_new":false,"window_days":14}`
)

func flakesServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.Contains(r.URL.Path, "flakes") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFlakesListPrintsTheDocumentWithoutInventingMetrics(t *testing.T) {
	srv := flakesServer(t, 200, `{"flakes":[`+flakeRows+`],"next_cursor":null,"notices":["metrics_unavailable"]}`)

	res := runBinary(t, t.TempDir(), srv.URL, "flakes", "list", "--org", "acme")

	want := `{"v":1,"flakes":[` + flakeRows + `],"next_cursor":null,"notices":["metrics_unavailable"]}` + "\n"
	if res.exit != 0 || res.stdout != want {
		t.Errorf("exit %d stdout %q want %q stderr %q", res.exit, res.stdout, want, res.stderr)
	}
}

func TestFlakesRefusalsExitByCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		exit   int
		code   string
	}{
		{"other org", 404, `{"error":"organization_not_found","message":"No such organization."}`, []string{"flakes", "list", "--org", "other"}, 3, "organization_not_found"},
		{"bad enum", 422, `{"error":"invalid_request","message":"sort must be one of: impactful, rate, recent, count."}`, []string{"flakes", "list", "--org", "acme", "--sort", "bogus"}, 5, "invalid_request"},
		{"bad token", 401, `{"error":"unauthorized","message":"Invalid token."}`, []string{"flakes", "list", "--org", "acme"}, 4, "unauthorized"},
		{"insufficient scope", 403, `{"error":"insufficient_scope","message":"This token can't read."}`, []string{"flakes", "list", "--org", "acme"}, 6, "insufficient_scope"},
		{"zero limit", 200, `{}`, []string{"flakes", "list", "--org", "acme", "--limit=0"}, 5, "usage_error"},
		{"negative limit", 200, `{}`, []string{"flakes", "list", "--org", "acme", "--limit=-1"}, 5, "usage_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := flakesServer(t, tc.status, tc.body)

			res := runBinary(t, t.TempDir(), srv.URL, append(tc.args, "--json")...)

			assertRefusal(t, res, tc.exit, tc.code)
		})
	}
}

func TestFlakesCommandsAreInTheSchemaWithTheirExitCodes(t *testing.T) {
	code, all, _ := execute("help", "--json")
	if code != 0 {
		t.Fatalf("help --json exit %d", code)
	}
	_, stdout, _ := execute("help", "flakes", "list", "--json")
	if !strings.Contains(all, `"path":"flakes list"`) || !strings.Contains(stdout, `"exit_codes":[0,3,4,5,6]`) {
		t.Errorf("flakes list missing from help --json, or exit codes wrong: %s", stdout)
	}
}
