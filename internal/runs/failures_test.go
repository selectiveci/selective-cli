package runs

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const failedRow = `{"id":"01-a","run_id":"run-1","suite":{"id":"suite-1","name":"web-rspec"},"test_id":"spec/a_spec.rb[1:1]","file":"spec/a_spec.rb","line":12,"error":"expected true, got false\n  # backtrace","node":"n1","outcome":"failed","attempts":1,"rerun_status":null,"flake_id":null,"flake_status":null,"provisional":true}`

const flakyRow = `{"id":"01-b","run_id":"run-2","suite":{"id":"suite-2","name":"api-minitest"},"test_id":null,"file":"test/b_test.rb","line":3,"error":null,"node":"n2","outcome":"flaky","attempts":2,"rerun_status":"passed","flake_id":"flake-1","flake_status":"quarantined","provisional":false}`

type stub struct {
	mu      sync.Mutex
	queries []url.Values
	pages   map[string]string
}

func (s *stub) env(t *testing.T, asJSON bool) (*setup.Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()
		if r.URL.Path != "/api/v1/builds/b-1/failures" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.pages[r.URL.Query().Get("cursor")]))
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	return &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut, JSON: asJSON}, &out, &errOut
}

func page(failures, next string) string {
	return `{"build_id":"b-1","status":"completed","archived":false,"archived_failed_count":0,"archived_flaky_failed_count":0,"failures":[` + failures + `],"next_cursor":` + next + `}`
}

func golden(t *testing.T, name string) string {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(want)
}

func TestFailuresJSONMatchesGolden(t *testing.T) {
	s := &stub{pages: map[string]string{"": page(failedRow+","+flakyRow, "null")}}
	env, out, _ := s.env(t, true)

	err := Failures(env, FailuresOptions{Build: "b-1", Outcome: "all"})

	if err != nil {
		t.Fatal(err)
	}
	if out.String() != golden(t, "failures.json") {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, golden(t, "failures.json"))
	}
}

func TestFailuresTextMatchesGolden(t *testing.T) {
	s := &stub{pages: map[string]string{"": page(failedRow+","+flakyRow, `"next-1"`)}}
	env, out, errOut := s.env(t, false)

	err := Failures(env, FailuresOptions{Build: "b-1", Outcome: "all", Limit: 2})

	if err != nil {
		t.Fatal(err)
	}
	if out.String() != golden(t, "failures.txt") {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, golden(t, "failures.txt"))
	}
	if errOut.String() != golden(t, "failures.stderr.txt") {
		t.Errorf("stderr:\n%s\nwant:\n%s", errOut, golden(t, "failures.stderr.txt"))
	}
}

func TestFailuresFollowsEveryCursorWithoutALimit(t *testing.T) {
	s := &stub{pages: map[string]string{
		"":   page(failedRow, `"c2"`),
		"c2": page(flakyRow, "null"),
	}}
	env, out, _ := s.env(t, true)

	err := Failures(env, FailuresOptions{Build: "b-1", Org: "acme", Outcome: "all"})

	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id":"01-a"`) || !strings.Contains(out.String(), `"id":"01-b"`) || !strings.HasSuffix(out.String(), `"next_cursor":null}`+"\n") {
		t.Errorf("stdout = %s", out)
	}
	if len(s.queries) != 2 || s.queries[0].Get("limit") != "200" || s.queries[1].Get("cursor") != "c2" ||
		s.queries[1].Get("org") != "acme" || s.queries[1].Get("outcome") != "all" {
		t.Errorf("queries = %v", s.queries)
	}
}

func TestFailuresWithALimitFetchesOnePageAndReturnsItsCursor(t *testing.T) {
	s := &stub{pages: map[string]string{"c1": page(failedRow, `"c2"`)}}
	env, out, _ := s.env(t, true)

	err := Failures(env, FailuresOptions{Build: "b-1", Limit: 1, Cursor: "c1"})

	if err != nil {
		t.Fatal(err)
	}
	if len(s.queries) != 1 || s.queries[0].Get("limit") != "1" || s.queries[0].Get("cursor") != "c1" {
		t.Errorf("queries = %v", s.queries)
	}
	if !strings.HasSuffix(out.String(), `"next_cursor":"c2"}`+"\n") {
		t.Errorf("stdout = %s", out)
	}
}

func TestFailuresSuiteFiltersByNameOrID(t *testing.T) {
	for _, suite := range []string{"api-minitest", "suite-2"} {
		t.Run(suite, func(t *testing.T) {
			s := &stub{pages: map[string]string{"": page(failedRow+","+flakyRow, "null")}}
			env, out, _ := s.env(t, true)

			err := Failures(env, FailuresOptions{Build: "b-1", Suite: suite})

			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), `"id":"01-a"`) || !strings.Contains(out.String(), `"id":"01-b"`) {
				t.Errorf("stdout = %s", out)
			}
		})
	}
}

func TestFailuresWithNoneWritesAnEmptyArrayInJSONAndAHintInText(t *testing.T) {
	archived := `{"build_id":"b-1","status":"completed","archived":true,"archived_failed_count":4,"archived_flaky_failed_count":1,"failures":[],"next_cursor":null}`
	s := &stub{pages: map[string]string{"": archived}}

	jsonEnv, jsonOut, _ := s.env(t, true)
	textEnv, textOut, textErr := s.env(t, false)

	if err := Failures(jsonEnv, FailuresOptions{Build: "b-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Failures(textEnv, FailuresOptions{Build: "b-1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut.String(), `"failures":[]`) {
		t.Errorf("json = %s", jsonOut)
	}
	if textOut.Len() != 0 || !strings.Contains(textErr.String(), "4 failed and 1 flaky") {
		t.Errorf("stdout %q, stderr %q", textOut, textErr)
	}
}
