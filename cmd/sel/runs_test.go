package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func failuresServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasSuffix(r.URL.Path, "/failures") {
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

func TestFailuresPrintsTheDocumentWhenPiped(t *testing.T) {
	srv := failuresServer(t, 200, `{"build_id":"b-1","status":"completed","archived":false,"archived_failed_count":0,"archived_flaky_failed_count":0,"failures":[{"id":"01-a","run_id":"r","suite":{"id":"s","name":"web"},"test_id":"t","file":"a.rb","line":1,"error":"boom","node":"n","outcome":"failed","attempts":1,"rerun_status":null,"flake_id":null,"flake_status":null,"provisional":false}],"next_cursor":null}`)

	res := runBinary(t, t.TempDir(), srv.URL, "failures", "b-1")

	if res.exit != 0 || !strings.HasPrefix(res.stdout, `{"v":1,"build_id":"b-1"`) || !strings.Contains(res.stdout, `"error":"boom"`) {
		t.Errorf("exit %d stdout %q stderr %q", res.exit, res.stdout, res.stderr)
	}
}

func TestFailuresRefusalsExitByCode(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		exit   int
		code   string
	}{
		{"unknown build", 404, `{"error":"not_found","message":"No build with this id."}`, []string{"failures", "nope"}, 3, "not_found"},
		{"ambiguous build", 409, `{"error":"ambiguous_build","message":"This build id exists in more than one organization. Pass org."}`, []string{"failures", "b-1"}, 5, "ambiguous_build"},
		{"bad token", 401, `{"error":"unauthorized","message":"Invalid token."}`, []string{"failures", "b-1"}, 4, "unauthorized"},
		{"bad outcome", 422, `{"error":"invalid_request","message":"outcome must be one of failed, flaky, all"}`, []string{"failures", "b-1", "--outcome", "bogus"}, 5, "invalid_request"},
		{"missing build argument", 200, `{}`, []string{"failures"}, 5, "usage_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := failuresServer(t, tc.status, tc.body)

			res := runBinary(t, t.TempDir(), srv.URL, append(tc.args, "--json")...)

			assertRefusal(t, res, tc.exit, tc.code)
			if tc.code == "usage_error" && !strings.Contains(res.stdout, "accepts 1 arg(s), received 0") {
				t.Errorf("stdout = %q", res.stdout)
			}
		})
	}
}

func TestFailuresListsItsExitCodesInTheSchema(t *testing.T) {
	code, stdout, _ := execute("help", "failures", "--json")

	if code != 0 || !strings.Contains(stdout, `"use":"failures BUILD"`) || !strings.Contains(stdout, `"exit_codes":[0,3,4,5,6]`) {
		t.Errorf("exit %d stdout %s", code, stdout)
	}
}

func TestFailuresRejectsALimitBelowOne(t *testing.T) {
	srv := failuresServer(t, 200, `{"build_id":"b-1","status":"completed","archived":false,"archived_failed_count":0,"archived_flaky_failed_count":0,"failures":[],"next_cursor":null}`)

	for _, limit := range []string{"0", "-1"} {
		t.Run(limit, func(t *testing.T) {
			res := runBinary(t, t.TempDir(), srv.URL, "failures", "b-1", "--limit="+limit, "--json")

			assertRefusal(t, res, 5, "usage_error")
		})
	}
}
