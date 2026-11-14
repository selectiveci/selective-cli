package runs

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const trendsBody = `{"range":"week","suite_id":null,"window":{"from":"2026-11-06T00:00:00Z","to":"2026-11-13T00:00:00Z","bucket":"day"},"summary":{"tests_count":0,"pass_rate":null,"failures_count":0,"flaky_count":0,"median_build_s":null,"previous":{"pass_rate":null,"failures_count":3,"median_build_s":41.5}},"top_failing":[]}`

type trendsStub struct {
	body    string
	mu      sync.Mutex
	paths   []string
	queries []url.Values
}

func (s *trendsStub) env(t *testing.T) (*setup.Env, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.queries = append(s.queries, r.URL.Query())
		s.mu.Unlock()
		if r.URL.Path != "/api/v1/orgs/acme/trends" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.document()))
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	return &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut, JSON: true}, &out
}

func (s *trendsStub) document() string {
	if s.body != "" {
		return s.body
	}
	return trendsBody
}

func TestTrendsPrintsTheDocumentKeepingNullsAndAddingNothing(t *testing.T) {
	s := &trendsStub{}
	env, out := s.env(t)

	err := Trends(env, TrendsOptions{Org: "acme"})

	if err != nil {
		t.Fatal(err)
	}
	if out.String() != golden(t, "trends.json") {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, golden(t, "trends.json"))
	}
}

func TestTrendsSendsOnlyTheOptionsItWasGiven(t *testing.T) {
	cases := []struct {
		name string
		opts TrendsOptions
		want url.Values
	}{
		{"nothing", TrendsOptions{Org: "acme"}, url.Values{}},
		{"everything", TrendsOptions{Org: "acme", Range: "day", Suite: "web", Section: "slowest", Limit: 5},
			url.Values{"range": {"day"}, "suite": {"web"}, "section": {"slowest"}, "limit": {"5"}}},
		{"a retired section name reaches the server to be refused", TrendsOptions{Org: "acme", Section: "pass_rate"}, url.Values{"section": {"pass_rate"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &trendsStub{}
			env, _ := s.env(t)

			if err := Trends(env, tc.opts); err != nil {
				t.Fatal(err)
			}

			if len(s.queries) != 1 || s.queries[0].Encode() != tc.want.Encode() {
				t.Errorf("queries = %v, want %v", s.queries, tc.want)
			}
		})
	}
}
