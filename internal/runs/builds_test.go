package runs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const runningBuild = `{"id":"b-2","title":"Fix login","status":"running","suite_count":1,"total":10,"failed":1,"flaky":0,"started_at":"2026-11-13T10:00:00Z","completed_at":null,"duration_ms":null,"archived":false}`

const finishedBuild = `{"id":"b-1","branch":"main","status":"success","suite_count":2,"total":40,"failed":0,"started_at":"2026-11-13T09:00:00Z","completed_at":"2026-11-13T09:05:00Z","duration_ms":300000,"archived":false}`

type buildsStub struct {
	mu      sync.Mutex
	paths   []string
	queries []url.Values
	pages   map[string]string
}

func (s *buildsStub) env(t *testing.T) (*setup.Env, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/orgs/acme/builds":
			_, _ = w.Write([]byte(s.pages[r.URL.Query().Get("cursor")]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	return &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut, JSON: true}, &out
}

func buildsPage(rows, next string) string {
	return `{"builds":[` + rows + `],"next_cursor":` + next + `}`
}

func TestBuildsJSONKeepsAbsentAndNullFieldsAsTheServerSentThem(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage(runningBuild+","+finishedBuild, "null")}}
	env, out := s.env(t)

	err := Builds(env, BuildsOptions{Org: "acme"})

	if err != nil {
		t.Fatal(err)
	}
	if out.String() != golden(t, "builds.json") {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, golden(t, "builds.json"))
	}
}

func TestBuildsWithNoneWritesAnEmptyArray(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage("", "null")}}
	env, out := s.env(t)

	if err := Builds(env, BuildsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	if out.String() != `{"v":1,"builds":[],"next_cursor":null}`+"\n" {
		t.Errorf("stdout = %s", out)
	}
}

func TestBuildsSendsOnlyTheFiltersItWasGiven(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		opts BuildsOptions
		want url.Values
	}{
		{"nothing", BuildsOptions{Org: "acme"}, url.Values{}},
		{"every filter", BuildsOptions{Org: "acme", Status: "failed", Flaky: &yes, Branch: "main", Limit: 5, Cursor: "c1"},
			url.Values{"status": {"failed"}, "flaky": {"true"}, "branch": {"main"}, "limit": {"5"}, "cursor": {"c1"}}},
		{"flaky false reaches the server to be refused", BuildsOptions{Org: "acme", Flaky: &no}, url.Values{"flaky": {"false"}}},
		{"status flaky reaches the server to be refused", BuildsOptions{Org: "acme", Status: "flaky"}, url.Values{"status": {"flaky"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &buildsStub{pages: map[string]string{tc.opts.Cursor: buildsPage("", "null")}}
			env, _ := s.env(t)

			if err := Builds(env, tc.opts); err != nil {
				t.Fatal(err)
			}

			if len(s.queries) != 1 || s.queries[0].Encode() != tc.want.Encode() {
				t.Errorf("queries = %v, want %v", s.queries, tc.want)
			}
		})
	}
}

func TestBuildsWithoutAllFetchesOnePageAndReturnsItsCursor(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage(finishedBuild, `"c2"`), "c2": buildsPage(runningBuild, "null")}}
	env, out := s.env(t)

	if err := Builds(env, BuildsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	if len(s.queries) != 1 || !strings.HasSuffix(out.String(), `"next_cursor":"c2"}`+"\n") || strings.Contains(out.String(), "notices") {
		t.Errorf("queries %v stdout %s", s.queries, out)
	}
}

func TestBuildsAllFollowsEveryCursorInOrderAtTheMaximumPageSize(t *testing.T) {
	s := &buildsStub{pages: map[string]string{
		"":   buildsPage(runningBuild, `"c2"`),
		"c2": buildsPage(finishedBuild, `"c3"`),
		"c3": buildsPage(strings.Replace(finishedBuild, `"b-1"`, `"b-0"`, 1), "null"),
	}}
	env, out := s.env(t)

	if err := Builds(env, BuildsOptions{Org: "acme", All: true}); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Builds     []struct{ ID string } `json:"builds"`
		NextCursor *string               `json:"next_cursor"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, b := range doc.Builds {
		ids = append(ids, b.ID)
	}
	cursors := []string{}
	for _, q := range s.queries {
		cursors = append(cursors, q.Get("cursor")+"@"+q.Get("limit"))
	}
	if !slices.Equal(ids, []string{"b-2", "b-1", "b-0"}) || doc.NextCursor != nil || !slices.Equal(cursors, []string{"@200", "c2@200", "c3@200"}) {
		t.Errorf("ids %v next %v requests %v", ids, doc.NextCursor, cursors)
	}
}

func TestBuildsAllTakesTheLimitAsThePageSize(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage(runningBuild, `"c2"`), "c2": buildsPage(finishedBuild, "null")}}
	env, _ := s.env(t)

	if err := Builds(env, BuildsOptions{Org: "acme", All: true, Limit: 1}); err != nil {
		t.Fatal(err)
	}

	if len(s.queries) != 2 || s.queries[0].Get("limit") != "1" || s.queries[1].Get("limit") != "1" {
		t.Errorf("queries = %v", s.queries)
	}
}

func walkBuilds(t *testing.T, opts BuildsOptions) (builds int, next *string, notices []string, requests int) {
	t.Helper()
	rows := strings.TrimSuffix(strings.Repeat(finishedBuild+",", 11), ",")
	s := &buildsStub{pages: map[string]string{"": buildsPage(rows, `"more"`), "more": buildsPage(rows, `"more"`)}}
	env, out := s.env(t)

	if err := Builds(env, opts); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Builds     []json.RawMessage `json:"builds"`
		NextCursor *string           `json:"next_cursor"`
		Notices    []string          `json:"notices"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return len(doc.Builds), doc.NextCursor, doc.Notices, len(s.queries)
}

func TestBuildsAllStopsAtTheItemCapKeepsTheCursorAndSaysSo(t *testing.T) {
	builds, next, notices, _ := walkBuilds(t, BuildsOptions{Org: "acme", All: true})

	if builds < maxFailures || builds >= maxFailures+11 {
		t.Errorf("collected %d builds from 11-row pages, want %d", builds, maxFailures)
	}
	if next == nil || *next != "more" || !slices.Equal(notices, []string{"pagination_stopped_early"}) {
		t.Errorf("next_cursor %v notices %v, want the cursor kept and pagination_stopped_early", next, notices)
	}
}

func TestBuildsAllStopsAtTheRequestCeilingWhenPagesNeverEnd(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage("", `"more"`), "more": buildsPage("", `"more"`)}}
	env, out := s.env(t)

	if err := Builds(env, BuildsOptions{Org: "acme", All: true}); err != nil {
		t.Fatal(err)
	}

	if len(s.queries) != maxRequests || !strings.Contains(out.String(), `"notices":["pagination_stopped_early"]`) {
		t.Errorf("requests %d stdout %s", len(s.queries), out)
	}
}

func TestBuildsWithoutAllNeverAddsTheStoppedEarlyNotice(t *testing.T) {
	_, next, notices, requests := walkBuilds(t, BuildsOptions{Org: "acme"})

	if next == nil || len(notices) != 0 || requests != 1 {
		t.Errorf("next_cursor %v notices %v requests %d", next, notices, requests)
	}
}
