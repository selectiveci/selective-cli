package runs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/setup"
)

const fullTrends = `{"range":"week","suite_id":"s-1","window":{"from":"2026-11-06T00:00:00Z","to":"2026-11-13T00:00:00Z","bucket":"day"},"summary":{"tests_count":120,"pass_rate":0.9333,"failures_count":8,"flaky_count":2,"median_build_s":41.5,"previous":{"pass_rate":0.9,"failures_count":3,"median_build_s":50}},"outcomes":[{"bucket_start":"2026-11-12T00:00:00Z","passed_count":5,"failed_count":1,"incomplete_count":0,"cancelled_count":0}],"failures":[{"bucket_start":"2026-11-12T00:00:00Z","failures_count":4}],"duration":[{"bucket_start":"2026-11-12T00:00:00Z","avg_build_s":null,"p95_build_s":null,"build_count":0}],"top_failing":[{"test_id":"t-1","test_name":"spec/a_spec.rb","suite_name":"web","fail_count":3,"run_count":10}],"most_flaky":[{"test_id":"t-2","test_name":"spec/b_spec.rb","suite_name":"web","status":"flaky","occurrence_count":4,"branch_count":2}],"slowest":[{"test_id":"t-3","test_name":"spec/c_spec.rb","suite_name":"web","avg_s":1.5,"p95_s":2.25,"max_s":3,"run_count":9,"trend":"up"}]}`

const absentTrends = `{"range":"day","suite_id":null,"summary":{"tests_count":5,"pass_rate":1,"failures_count":0,"previous":{"pass_rate":null,"failures_count":0}}}`

const emptySectionTrends = `{"range":"day","suite_id":null,"window":{"from":"2026-11-12T00:00:00Z","to":"2026-11-13T00:00:00Z","bucket":"hour"},"top_failing":[]}`

func asText(env *setup.Env, out *bytes.Buffer) (*bytes.Buffer, *bytes.Buffer) {
	env.JSON = false
	return out, env.Stderr.(*bytes.Buffer)
}

func assertText(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
	}
}

func TestBuildsTextMatchesGoldenAndPointsAtTheNextPage(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage(runningBuild+","+finishedBuild, `"c2"`)}}
	env, out := s.env(t)
	stdout, stderr := asText(env, out)

	if err := Builds(env, BuildsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	assertText(t, "stdout", stdout.String(), golden(t, "builds.txt"))
	assertText(t, "stderr", stderr.String(), "More results: sel builds --org 'acme' --cursor 'c2'\n")
}

func TestBuildsTextPrintsUnknownNotZeroForAFieldTheServerLeftOut(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage(finishedBuild, "null")}}
	env, out := s.env(t)
	stdout, _ := asText(env, out)

	if err := Builds(env, BuildsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	cells := strings.Fields(strings.Split(stdout.String(), "\n")[1])
	if len(cells) != 9 || cells[5] != "unknown" || cells[8] != "unknown" {
		t.Errorf("row cells = %q, want FLAKY and TITLE unknown", cells)
	}
}

func TestBuildsTextWithNoneSaysSoOnStderr(t *testing.T) {
	s := &buildsStub{pages: map[string]string{"": buildsPage("", "null")}}
	env, out := s.env(t)
	stdout, stderr := asText(env, out)

	if err := Builds(env, BuildsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	if stdout.Len() != 0 || stderr.String() != "No builds match.\n" {
		t.Errorf("stdout %q stderr %q", stdout, stderr)
	}
}

func TestBuildsTextExplainsAnEarlyStopAndKeepsTheContinuation(t *testing.T) {
	rows := strings.TrimSuffix(strings.Repeat(finishedBuild+",", 11), ",")
	s := &buildsStub{pages: map[string]string{"": buildsPage(rows, `"more"`), "more": buildsPage(rows, `"more"`)}}
	env, out := s.env(t)
	_, stderr := asText(env, out)

	if err := Builds(env, BuildsOptions{Org: "acme", All: true}); err != nil {
		t.Fatal(err)
	}

	want := "Notice: stopped early after 20009 builds; run the command below to continue\nMore results: sel builds --org 'acme' --all --cursor 'more'\n"
	if !strings.HasSuffix(stderr.String(), want) {
		t.Errorf("stderr %q, want suffix %q", stderr, want)
	}
}

func TestBuildsContinuationKeepsEveryFilterAndQuotesEveryValue(t *testing.T) {
	yes := true
	s := &buildsStub{pages: map[string]string{"": buildsPage(finishedBuild, `"it's"`)}}
	env, out := s.env(t)
	_, stderr := asText(env, out)

	err := Builds(env, BuildsOptions{Org: "acme", Hostname: "h.example", Status: "failed", Flaky: &yes, Branch: "feat/x y", Limit: 5})

	if err != nil {
		t.Fatal(err)
	}
	want := "More results: sel builds --hostname 'h.example' --org 'acme' --status 'failed' --flaky=true --branch 'feat/x y' --limit 5 --cursor 'it'\\''s'\n"
	if stderr.String() != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
}

func TestBuildTextMatchesGolden(t *testing.T) {
	s := &buildsStub{detail: buildDetail}
	env, out := s.env(t)
	stdout, stderr := asText(env, out)

	if err := Build(env, BuildOptions{ID: "b-2"}); err != nil {
		t.Fatal(err)
	}

	assertText(t, "stdout", stdout.String(), golden(t, "build.txt"))
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestTrendsTextMatchesGoldenAndNamesTheWindowOnStderr(t *testing.T) {
	s := &trendsStub{body: fullTrends}
	env, out := s.env(t)
	stdout, stderr := asText(env, out)

	if err := Trends(env, TrendsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	assertText(t, "stdout", stdout.String(), golden(t, "trends-full.txt"))
	assertText(t, "stderr", stderr.String(), "Window: week, 2026-11-06T00:00:00Z to 2026-11-13T00:00:00Z, in day buckets.\n")
}

func TestTrendsTextTellsUnknownFromEmptyAndNeverPrintsZeroForEither(t *testing.T) {
	s := &trendsStub{body: absentTrends}
	env, out := s.env(t)
	stdout, _ := asText(env, out)

	if err := Trends(env, TrendsOptions{Org: "acme"}); err != nil {
		t.Fatal(err)
	}

	assertText(t, "stdout", stdout.String(), golden(t, "trends-empty.txt"))
}

func TestTrendsTextSaysWhenASectionHasNoRows(t *testing.T) {
	s := &trendsStub{body: emptySectionTrends}
	env, out := s.env(t)
	stdout, stderr := asText(env, out)

	if err := Trends(env, TrendsOptions{Org: "acme", Section: "top_failing"}); err != nil {
		t.Fatal(err)
	}

	if stdout.String() != "TOP FAILING\n" || !strings.HasSuffix(stderr.String(), "No rows in top_failing.\n") {
		t.Errorf("stdout %q stderr %q", stdout, stderr)
	}
}
