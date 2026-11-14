package runs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"slices"
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

func TestFailuresTextKeepsTheContinuationWhenSuiteFilteringEmptiesThePage(t *testing.T) {
	s := &stub{pages: map[string]string{"": page(failedRow, `"next-1"`)}}
	jsonEnv, jsonOut, _ := s.env(t, true)
	textEnv, textOut, textErr := s.env(t, false)
	opts := FailuresOptions{Build: "b-1", Suite: "api-minitest", Limit: 1}

	if err := Failures(jsonEnv, opts); err != nil {
		t.Fatal(err)
	}
	if err := Failures(textEnv, opts); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(jsonOut.String(), `"failures":[]`) || !strings.HasSuffix(jsonOut.String(), `"next_cursor":"next-1"}`+"\n") {
		t.Errorf("json = %s", jsonOut)
	}
	want := "More results: sel failures 'b-1' --suite 'api-minitest' --limit 1 --cursor 'next-1'\n"
	if textOut.Len() != 0 || !strings.HasSuffix(textErr.String(), want) {
		t.Errorf("stdout %q, stderr %q, want stderr ending %q", textOut, textErr, want)
	}
}

func TestFailuresContinuationKeepsHostnameAndOrgAndShellQuotesEveryValue(t *testing.T) {
	cases := map[string]string{
		"$VERSION":    `'$VERSION'`,
		"$(id)":       `'$(id)'`,
		"it's `x`":    `'it'\''s ` + "`x`" + `'`,
		"two  words;": `'two  words;'`,
	}
	for suite, quoted := range cases {
		t.Run(suite, func(t *testing.T) {
			s := &stub{pages: map[string]string{"": page(failedRow, `"c$2"`)}}
			env, _, errOut := s.env(t, false)

			err := Failures(env, FailuresOptions{Build: "b-1", Hostname: "http://h:4000", Org: "ac me", Suite: suite, Limit: 1})

			if err != nil {
				t.Fatal(err)
			}
			want := "More results: sel failures 'b-1' --hostname 'http://h:4000' --org 'ac me' --suite " + quoted + " --limit 1 --cursor 'c$2'\n"
			if !strings.HasSuffix(errOut.String(), want) {
				t.Errorf("stderr %q, want suffix %q", errOut, want)
			}
		})
	}
}

func TestQuotersEscapeAnEmbeddedSingleQuoteForTheirShell(t *testing.T) {
	cases := []struct{ goos, value, want string }{
		{"linux", "it's $(id)", `'it'\''s $(id)'`},
		{"darwin", "it's", `'it'\''s'`},
		{"windows", "it's $(id)", `'it''s $(id)'`},
		{"windows", "plain", `'plain'`},
	}
	for _, tc := range cases {
		if got := quoteFor(tc.goos, tc.value); got != tc.want {
			t.Errorf("quoteFor(%q, %q) = %s, want %s", tc.goos, tc.value, got, tc.want)
		}
	}
}

func TestContinuationHintUsesTheHostShellQuoting(t *testing.T) {
	hostOS = "windows"
	t.Cleanup(func() { hostOS = runtime.GOOS })
	s := &stub{pages: map[string]string{"": page(failedRow, `"c'2"`)}}
	env, _, errOut := s.env(t, false)

	err := Failures(env, FailuresOptions{Build: "b-1", Suite: "it's", Limit: 1})

	if err != nil {
		t.Fatal(err)
	}
	want := "More results: sel failures 'b-1' --suite 'it''s' --limit 1 --cursor 'c''2'\n"
	if !strings.HasSuffix(errOut.String(), want) {
		t.Errorf("stderr %q, want suffix %q", errOut, want)
	}
}

func TestFailuresTakesPageMetadataFromTheLastPageFetched(t *testing.T) {
	running := `{"build_id":"b-1","status":"running","archived":false,"archived_failed_count":0,"archived_flaky_failed_count":0,"failures":[` + failedRow + `],"next_cursor":"c2"}`
	archived := `{"build_id":"b-1","status":"completed","archived":true,"archived_failed_count":3,"archived_flaky_failed_count":1,"failures":[` + flakyRow + `],"next_cursor":null}`
	s := &stub{pages: map[string]string{"": running, "c2": archived}}
	env, out, _ := s.env(t, true)

	err := Failures(env, FailuresOptions{Build: "b-1", Outcome: "all"})

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status":"completed"`, `"archived":true`, `"archived_failed_count":3`, `"archived_flaky_failed_count":1`, `"id":"01-a"`, `"id":"01-b"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %s: %s", want, out)
		}
	}
}

func walkNotices(t *testing.T, rows string, opts FailuresOptions) (failures int, next *string, notices []string, requests int) {
	t.Helper()
	s := &stub{pages: map[string]string{"": page(rows, `"more"`), "more": page(rows, `"more"`)}}
	env, out, _ := s.env(t, true)

	if err := Failures(env, opts); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Failures   []json.RawMessage `json:"failures"`
		NextCursor *string           `json:"next_cursor"`
		Notices    []string          `json:"notices"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return len(doc.Failures), doc.NextCursor, doc.Notices, len(s.queries)
}

func TestFailuresCapsAutoPaginationByCountSoShortPagesStillReachTheTotal(t *testing.T) {
	rows := strings.TrimSuffix(strings.Repeat(failedRow+",", 11), ",")

	failures, next, notices, _ := walkNotices(t, rows, FailuresOptions{Build: "b-1"})

	if failures < maxFailures || failures >= maxFailures+11 {
		t.Errorf("collected %d failures from 11-row pages, want %d", failures, maxFailures)
	}
	if next == nil || *next != "more" || !slices.Contains(notices, "pagination_stopped_early") {
		t.Errorf("next_cursor %v notices %v, want the cursor kept and pagination_stopped_early", next, notices)
	}
}

func TestFailuresStopsAtTheRequestCeilingWhenNoRowSurvivesTheSuiteFilter(t *testing.T) {
	failures, next, notices, requests := walkNotices(t, failedRow, FailuresOptions{Build: "b-1", Suite: "other"})

	if requests != maxRequests || failures != 0 {
		t.Errorf("requests %d failures %d, want %d requests and none", requests, failures, maxRequests)
	}
	if next == nil || !slices.Contains(notices, "pagination_stopped_early") {
		t.Errorf("next_cursor %v notices %v", next, notices)
	}
}

func TestFailuresWithALimitNeverAddsTheStoppedEarlyNotice(t *testing.T) {
	_, next, notices, _ := walkNotices(t, failedRow, FailuresOptions{Build: "b-1", Limit: 1})

	if next == nil || len(notices) != 0 {
		t.Errorf("next_cursor %v notices %v", next, notices)
	}
}

func TestFailuresTextExplainsAnEarlyStopAndKeepsTheContinuation(t *testing.T) {
	rows := strings.TrimSuffix(strings.Repeat(failedRow+",", 11), ",")
	s := &stub{pages: map[string]string{"": page(rows, `"more"`), "more": page(rows, `"more"`)}}
	env, _, errOut := s.env(t, false)

	if err := Failures(env, FailuresOptions{Build: "b-1"}); err != nil {
		t.Fatal(err)
	}

	want := "Notice: stopped early after 20009 failures; run the command below to continue\nMore results: sel failures 'b-1' --cursor 'more'\n"
	if !strings.HasSuffix(errOut.String(), want) {
		t.Errorf("stderr %q, want suffix %q", errOut, want)
	}
}
