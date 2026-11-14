package runs

import (
	"bytes"
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
