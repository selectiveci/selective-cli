package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/localrun"
)

const (
	rspecLock    = "    rspec-core (3.13.0)\n    selective-ruby-core (0.3.0)\n"
	oldCoreLock  = "    rspec-core (3.13.0)\n    selective-ruby-core (0.2.9)\n"
	minitestLock = "    selective-ruby-minitest (0.3.0)\n    selective-ruby-core (0.3.0)\n"
)

func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runnerOf(root string, suiteTypes ...string) Check {
	s := &state{opts: Options{Root: func() (string, error) { return root, nil }, OS: "linux"}, suiteTypes: suiteTypes}
	return s.checkRunner()
}

func withClientOnPath(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake client is a shell script")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("VIRTUAL_ENV", "")
	marker := filepath.Join(t.TempDir(), "ran")
	body := "#!/bin/sh\n: selective_pytest\n" + strings.ReplaceAll(script, "MARKER", marker) + "\n"
	for _, name := range []string{"selective", "bundle"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return marker
}

func TestRunnerWarnsWithSelTestsOwnNoFrameworkError(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"nothing detected": {},
		"ambiguous":        {"Gemfile": "gem 'rspec-core'", "pytest.ini": ""},
	} {
		t.Run(name, func(t *testing.T) {
			root := project(t, files)
			_, want := localrun.DetectFramework(root, "")

			c := runnerOf(root)

			if c.Status != Warn || c.Code != contract.NoFramework || want == nil || c.Message != want.Error() {
				t.Errorf("check = %+v, want a no_framework warn carrying %v", c, want)
			}
		})
	}
}

func TestRunnerWarnsWithSelTestsOwnRunnerMissingError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("VIRTUAL_ENV", "")
	for name, tc := range map[string]struct {
		files map[string]string
		fw    localrun.Framework
	}{
		"ruby core too old":     {map[string]string{"Gemfile.lock": oldCoreLock}, localrun.RSpec},
		"pytest with no client": {map[string]string{"pytest.ini": ""}, localrun.Pytest},
	} {
		t.Run(name, func(t *testing.T) {
			root := project(t, tc.files)
			_, want := localrun.ClientCommand(root, tc.fw, os.Environ())

			c := runnerOf(root)

			if c.Status != Warn || c.Code != contract.RunnerMissing || want == nil || c.Message != want.Error() {
				t.Errorf("check = %+v, want a runner_missing warn carrying %v", c, want)
			}
		})
	}
}

func TestRunnerIsOkAndNamesTheFramework(t *testing.T) {
	withClientOnPath(t, "exit 0")
	for fw, files := range map[string]map[string]string{
		"rspec":    {"Gemfile.lock": rspecLock},
		"minitest": {"Gemfile.lock": minitestLock},
		"pytest":   {"pytest.ini": ""},
	} {
		c := runnerOf(project(t, files))

		if c.Status != OK || c.Detail["framework"] != fw || !strings.Contains(c.Message, fw) {
			t.Errorf("%s: check = %+v", fw, c)
		}
	}
}

func TestRunnerStartsNoProcess(t *testing.T) {
	marker := withClientOnPath(t, ": > MARKER")
	for name, files := range map[string]map[string]string{
		"ruby":   {"Gemfile.lock": rspecLock},
		"pytest": {"pytest.ini": ""},
	} {
		c := runnerOf(project(t, files))

		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) || c.Status != OK {
			t.Errorf("%s: a client was executed (stat err %v) or never found: %+v", name, err, c)
		}
	}
}

func TestRunnerWarnsOnWindowsWhateverTheHostIs(t *testing.T) {
	s := &state{opts: Options{Root: func() (string, error) { return project(t, map[string]string{"Gemfile.lock": rspecLock}), nil }, OS: "windows"}}

	c := s.checkRunner()

	if c.Status != Warn || c.Message != localrun.UnsupportedOS("windows").Error() {
		t.Errorf("check = %+v", c)
	}
}

func TestRunnerComparesSuiteTypesWithTheDetectedFramework(t *testing.T) {
	root := project(t, map[string]string{"Gemfile.lock": rspecLock})
	cases := []struct {
		name   string
		types  []string
		status Status
		reason string
	}{
		{"no suite known", nil, OK, ""},
		{"same type", []string{"rspec"}, OK, ""},
		{"one of several matches", []string{"pytest", "rspec"}, OK, ""},
		{"different runnable type", []string{"pytest"}, Warn, "looks like rspec"},
		{"type with no local runner", []string{"vitest"}, Warn, "no local runner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := runnerOf(root, tc.types...)

			if c.Status != tc.status || !strings.Contains(c.Message, tc.reason) {
				t.Errorf("check = %+v, want %s mentioning %q", c, tc.status, tc.reason)
			}
		})
	}
}

func TestRunnerNeverFails(t *testing.T) {
	broken := func() (string, error) { return "", errors.New("no cwd") }
	missing := func() (string, error) { return filepath.Join(t.TempDir(), "gone"), nil }
	ready := func() (string, error) { return project(t, map[string]string{"Gemfile.lock": rspecLock}), nil }
	for name, s := range map[string]*state{
		"unreadable cwd": {opts: Options{Root: broken, OS: "linux"}},
		"missing root":   {opts: Options{Root: missing, OS: "linux"}},
		"windows":        {opts: Options{Root: ready, OS: "windows"}},
		"suite mismatch": {opts: Options{Root: ready, OS: "linux"}, suiteTypes: []string{"junit"}},
	} {
		if c := s.checkRunner(); c.Status == Fail || c.Status == Skip {
			t.Errorf("%s: check = %+v", name, c)
		}
	}
}

func TestRunnerUsesTheSuiteTypesTheSuiteCheckFound(t *testing.T) {
	vitest := strings.Replace(suites, `"type":"rspec"`, `"type":"vitest"`, 1)
	mismatched := run(t, serve(t, with("/api/v1/orgs/acme/suites", reply(200, vitest))), Options{})
	noSuite := run(t, serve(t, with("/api/v1/orgs/acme/suites", reply(200, `{"suites":[]}`))), Options{})

	if got, want := shape(mismatched), "config=ok server=ok auth=ok org=ok suite=ok socket=skip runner=warn cli_version=skip"; got != want || mismatched.Err() != nil {
		t.Errorf("checks = %s, err %v, want %s", got, mismatched.Err(), want)
	}
	if got, want := shape(noSuite), "config=ok server=ok auth=ok org=ok suite=fail socket=skip runner=ok cli_version=skip"; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
}

func TestRunnerWarningLeavesTheExitCodeAlone(t *testing.T) {
	bare := project(t, nil)

	report := run(t, serve(t, healthy()), Options{Root: func() (string, error) { return bare, nil }})

	if c := check(t, report, "runner"); c.Status != Warn || c.Code != contract.NoFramework || report.Status != "warn" || report.Err() != nil {
		t.Errorf("runner = %+v, status %q, err %v, want a warn that leaves the exit at 0", c, report.Status, report.Err())
	}
}
