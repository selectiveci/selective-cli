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

func TestFlakesShowAndPromptPrintTheirDocumentsWhenPiped(t *testing.T) {
	show := flakesServer(t, 200, `{"flake":`+flakeRows+`}`)
	prompt := flakesServer(t, 200, `{"prompt":{"flake_id":"`+flakeID+`","text":"Fix it\n"}}`)

	shown := runBinary(t, t.TempDir(), show.URL, "flakes", "show", flakeID)
	prompted := runBinary(t, t.TempDir(), prompt.URL, "flakes", "prompt", flakeID)
	raw := runBinary(t, t.TempDir(), prompt.URL, "flakes", "prompt", flakeID, "--text")

	if shown.exit != 0 || shown.stdout != `{"v":1,"flake":`+flakeRows+`}`+"\n" {
		t.Errorf("show: exit %d stdout %q", shown.exit, shown.stdout)
	}
	if prompted.exit != 0 || prompted.stdout != `{"v":1,"prompt":{"flake_id":"`+flakeID+`","text":"Fix it\n"}}`+"\n" {
		t.Errorf("prompt: exit %d stdout %q", prompted.exit, prompted.stdout)
	}
	if raw.exit != 0 || raw.stdout != "Fix it\n" {
		t.Errorf("prompt --text: exit %d stdout %q", raw.exit, raw.stdout)
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
		{"unknown flake", 404, `{"error":"not_found","message":"Flake not found."}`, []string{"flakes", "show", flakeID}, 3, "not_found"},
		{"unknown flake prompt", 404, `{"error":"not_found","message":"Flake not found."}`, []string{"flakes", "prompt", flakeID}, 3, "not_found"},
		{"other org", 404, `{"error":"organization_not_found","message":"No such organization."}`, []string{"flakes", "list", "--org", "other"}, 3, "organization_not_found"},
		{"bad enum", 422, `{"error":"invalid_request","message":"sort must be one of: impactful, rate, recent, count."}`, []string{"flakes", "list", "--org", "acme", "--sort", "bogus"}, 5, "invalid_request"},
		{"bad token", 401, `{"error":"unauthorized","message":"Invalid token."}`, []string{"flakes", "list", "--org", "acme"}, 4, "unauthorized"},
		{"read-only scope missing", 403, `{"error":"insufficient_scope","message":"This token can't read."}`, []string{"flakes", "show", flakeID}, 6, "insufficient_scope"},
		{"missing id", 200, `{}`, []string{"flakes", "show"}, 5, "usage_error"},
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
	for _, name := range []string{"list", "show", "prompt"} {
		_, stdout, _ := execute("help", "flakes", name, "--json")

		if !strings.Contains(all, `"path":"flakes `+name+`"`) || !strings.Contains(stdout, `"exit_codes":[0,3,4,5,6]`) {
			t.Errorf("%s: missing from help --json, or exit codes wrong: %s", name, stdout)
		}
	}
}
