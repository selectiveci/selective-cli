package runs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/setup"
)

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
