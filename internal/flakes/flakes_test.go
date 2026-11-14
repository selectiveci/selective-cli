package flakes

import (
	"bytes"
	"encoding/json"
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

const (
	measuredFlake   = `{"id":"11111111-1111-4111-8111-111111111111","status":"flaking","test":{"id":"t1","name":"checkout flow","path":"spec/checkout_spec.rb"},"suite":{"id":"s1","name":"web-rspec"},"first_seen_at":"2026-11-01T08:00:00Z","last_seen_at":"2026-11-12T16:30:00Z","flake_rate":0.125,"occurrence_count":7,"retry_minutes":4.5,"sparkline":[0,1,2],"is_new":false,"impact_score":8.3,"window_days":14}`
	unmeasuredFlake = `{"id":"22222222-2222-4222-8222-222222222222","status":"quarantined","test":{"id":"t2","name":"login","path":"spec/login_spec.rb"},"suite":{"id":"s1","name":"web-rspec"},"first_seen_at":"2026-11-02T08:00:00Z","last_seen_at":"2026-11-12T09:05:00Z","occurrence_count":3,"sparkline":[1,0,0],"is_new":true,"window_days":14}`
	zeroFlake       = `{"id":"33333333-3333-4333-8333-333333333333","status":"flaking","test":{"id":"t3","name":"search","path":"spec/search_spec.rb"},"suite":{"id":"s2","name":"api-minitest"},"first_seen_at":"2026-11-03T08:00:00Z","last_seen_at":"2026-11-10T00:00:00Z","flake_rate":0.0,"occurrence_count":1,"retry_minutes":0.0,"sparkline":[0,0,1],"is_new":false,"impact_score":0.0,"window_days":14}`
	detailFlake     = `{"id":"11111111-1111-4111-8111-111111111111","status":"flaking","test":{"id":"t1","name":"checkout flow","path":"spec/checkout_spec.rb"},"suite":{"id":"s1","name":"web-rspec"},"first_seen_at":"2026-11-01T08:00:00Z","last_seen_at":"2026-11-12T16:30:00Z","flake_rate":0.125,"occurrence_count":7,"retry_minutes":4.5,"sparkline":[0,1,2],"is_new":false,"impact_score":8.3,"window_days":14,"org_slug":"acme","occurrences":[{"id":"o1","detected_at":"2026-11-12T16:30:00Z","branch":"main","commit_sha":"abcdef0123456789","build_id":"b-77","run_id":"r1","actor":"ada","duration_s":61.5,"failure_message":"expected 200, got 500\n  # backtrace"},{"id":"o2","detected_at":"2026-11-11T10:00:00Z","branch":"fix/cart","commit_sha":"0123456789abcdef","build_id":"b-70","run_id":"r2","actor":null,"duration_s":null,"failure_message":null}]}`
	promptText      = "Fix the flaky test \"checkout flow\".\n\nRecent failures:\n- ada on main at abcdef0\n"
)

type stub struct {
	mu      sync.Mutex
	queries []url.Values
	pages   map[string]string
	prompt  string
}

func (s *stub) env(t *testing.T, asJSON bool) (*setup.Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/orgs/acme/flakes":
			_, _ = w.Write([]byte(s.pages[r.URL.Query().Get("cursor")]))
		case "/api/v1/flakes/f-1":
			_, _ = w.Write([]byte(`{"flake":` + detailFlake + `}`))
		case "/api/v1/flakes/f-1/prompt":
			body, _ := json.Marshal(map[string]any{"prompt": map[string]string{"flake_id": "f-1", "text": s.prompt}})
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	return &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut, JSON: asJSON}, &out, &errOut
}

func page(flakes, next, notices string) string {
	return `{"flakes":[` + flakes + `],"next_cursor":` + next + `,"notices":` + notices + `}`
}

func golden(t *testing.T, name string) string {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(want)
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	if got != golden(t, name) {
		t.Errorf("%s:\n%s\nwant:\n%s", name, got, golden(t, name))
	}
}

func TestListJSONMatchesGolden(t *testing.T) {
	s := &stub{pages: map[string]string{"": page(measuredFlake+","+unmeasuredFlake+","+zeroFlake, "null", `["metrics_unavailable"]`)}}
	env, out, _ := s.env(t, true)

	err := List(env, ListOptions{Org: "acme"})

	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "list.json", out.String())
}

func TestListKeepsUncomputedMetricsAbsentAndComputedZerosPresent(t *testing.T) {
	s := &stub{pages: map[string]string{"": page(unmeasuredFlake+","+zeroFlake, "null", `["metrics_unavailable"]`)}}
	env, out, _ := s.env(t, true)

	if err := List(env, ListOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Flakes  []map[string]any `json:"flakes"`
		Notices []string         `json:"notices"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"flake_rate", "retry_minutes", "impact_score"} {
		if _, present := doc.Flakes[0][key]; present {
			t.Errorf("uncomputed flake carries %s = %v, want the key absent", key, doc.Flakes[0][key])
		}
		if value, present := doc.Flakes[1][key]; !present || value != 0.0 {
			t.Errorf("computed zero flake %s = %v (present %v), want 0", key, value, present)
		}
	}
	if len(doc.Notices) != 1 || doc.Notices[0] != "metrics_unavailable" {
		t.Errorf("notices = %v", doc.Notices)
	}
}

func TestListTextMatchesGolden(t *testing.T) {
	s := &stub{pages: map[string]string{"": page(measuredFlake+","+unmeasuredFlake+","+zeroFlake, `"next-1"`, `["approximate_ranking","metrics_unavailable"]`)}}
	env, out, errOut := s.env(t, false)

	err := List(env, ListOptions{Org: "acme", Hostname: "http://h", FlakesQuery: api.FlakesQuery{Suite: "web rspec", Sort: "rate", Limit: 3, Search: "it's"}})

	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "list.txt", out.String())
	assertGolden(t, "list.stderr.txt", errOut.String())
}

func TestListFollowsEveryCursorThroughShortPagesAndUnionsNotices(t *testing.T) {
	s := &stub{pages: map[string]string{
		"":   page(measuredFlake, `"c2"`, `["approximate_ranking"]`),
		"c2": page("", `"c3"`, `["approximate_ranking"]`),
		"c3": page(unmeasuredFlake, "null", `["approximate_ranking","metrics_unavailable"]`),
	}}
	env, out, _ := s.env(t, true)

	err := List(env, ListOptions{Org: "acme", FlakesQuery: api.FlakesQuery{Suite: "web-rspec", Status: "all"}})

	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Flakes     []json.RawMessage `json:"flakes"`
		NextCursor *string           `json:"next_cursor"`
		Notices    []string          `json:"notices"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Flakes) != 2 || doc.NextCursor != nil || strings.Join(doc.Notices, ",") != "approximate_ranking,metrics_unavailable" {
		t.Errorf("flakes %d next_cursor %v notices %v", len(doc.Flakes), doc.NextCursor, doc.Notices)
	}
	if len(s.queries) != 3 || s.queries[0].Get("limit") != "200" || s.queries[1].Get("cursor") != "c2" || s.queries[2].Get("cursor") != "c3" {
		t.Fatalf("queries = %v", s.queries)
	}
	for _, q := range s.queries {
		if q.Get("suite") != "web-rspec" || q.Get("status") != "all" {
			t.Errorf("filters lost on %v", q)
		}
	}
}

func TestListWithALimitFetchesOnePageAndReturnsItsCursor(t *testing.T) {
	s := &stub{pages: map[string]string{"c1": page(measuredFlake, `"c2"`, `[]`)}}
	env, out, _ := s.env(t, true)

	err := List(env, ListOptions{Org: "acme", FlakesQuery: api.FlakesQuery{Limit: 1, Cursor: "c1"}})

	if err != nil {
		t.Fatal(err)
	}
	if len(s.queries) != 1 || s.queries[0].Get("limit") != "1" || s.queries[0].Get("cursor") != "c1" || !strings.Contains(out.String(), `"next_cursor":"c2"`) {
		t.Errorf("queries = %v stdout = %s", s.queries, out)
	}
}

func TestListWithNoFlakesIsAnEmptyArrayNotNull(t *testing.T) {
	s := &stub{pages: map[string]string{"": page("", "null", "[]")}}
	env, out, _ := s.env(t, true)

	if err := List(env, ListOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	if out.String() != `{"v":1,"flakes":[],"next_cursor":null,"notices":[]}`+"\n" {
		t.Errorf("stdout = %s", out)
	}
}

func TestShowJSONMatchesGolden(t *testing.T) {
	s := &stub{}
	env, out, _ := s.env(t, true)
	history := 2

	err := Show(env, "f-1", &history)

	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "show.json", out.String())
	if s.queries[0].Get("history") != "2" {
		t.Errorf("query = %v", s.queries[0])
	}
}

func TestShowTextMatchesGolden(t *testing.T) {
	s := &stub{}
	env, out, _ := s.env(t, false)

	if err := Show(env, "f-1", nil); err != nil {
		t.Fatal(err)
	}

	assertGolden(t, "show.txt", out.String())
	if s.queries[0].Has("history") {
		t.Errorf("query = %v, want no history", s.queries[0])
	}
}

func TestPromptPrintsTheServerTextByteForByte(t *testing.T) {
	for _, text := range []string{promptText, strings.TrimRight(promptText, "\n"), "tabs\t & <html> \"quotes\" ünï\n\n"} {
		for name, asJSON := range map[string]bool{"terminal": false, "forced with --text": true} {
			t.Run(name, func(t *testing.T) {
				s := &stub{prompt: text}
				env, out, errOut := s.env(t, asJSON)

				if err := Prompt(env, "f-1", asJSON); err != nil {
					t.Fatal(err)
				}

				if out.String() != text || errOut.Len() != 0 {
					t.Errorf("stdout %q stderr %q, want exactly %q", out, errOut, text)
				}
			})
		}
	}
}

func TestPromptJSONIsTheServerObject(t *testing.T) {
	s := &stub{prompt: promptText}
	env, out, _ := s.env(t, true)

	if err := Prompt(env, "f-1", false); err != nil {
		t.Fatal(err)
	}

	assertGolden(t, "prompt.json", out.String())
}
