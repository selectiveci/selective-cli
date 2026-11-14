package localrun

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type Framework string

const (
	RSpec    Framework = "rspec"
	Minitest Framework = "minitest"
	Pytest   Framework = "pytest"
)

const minRubyCore = "0.3.0"

type marker struct{ file, needle string }

var detectors = []struct {
	framework Framework
	markers   []marker
}{
	{RSpec, []marker{{"Gemfile", "rspec-core"}, {"Gemfile.lock", "rspec-core"}}},
	{Minitest, []marker{{"Gemfile", "selective-ruby-minitest"}, {"Gemfile.lock", "selective-ruby-minitest"}}},
	{Pytest, []marker{{"pyproject.toml", "pytest"}, {"pytest.ini", ""}, {"setup.cfg", "pytest"}, {"tox.ini", "pytest"}}},
}

var rubyCoreSpec = regexp.MustCompile(`(?m)^\s+selective-ruby-core \((\d+)\.(\d+)\.(\d+)[^)]*\)`)

func DetectFramework(root, explicit string) (Framework, error) {
	if explicit != "" {
		for _, d := range detectors {
			if string(d.framework) == explicit {
				return d.framework, nil
			}
		}
		return "", contract.Errorf(contract.UsageError, "unknown framework %q; sel test supports %s", explicit, supported())
	}
	var found []string
	var framework Framework
	for _, d := range detectors {
		if hasMarker(root, d.markers) {
			found = append(found, string(d.framework))
			framework = d.framework
		}
	}
	switch len(found) {
	case 1:
		return framework, nil
	case 0:
		return "", contract.Errorf(contract.NoFramework, "no supported test framework found in %s (looked for %s); pass --framework to choose one", root, supported())
	}
	return "", contract.Errorf(contract.NoFramework, "found several test frameworks in %s: %s; pass --framework to choose one", root, strings.Join(found, ", "))
}

func supported() string {
	names := make([]string, len(detectors))
	for i, d := range detectors {
		names[i] = string(d.framework)
	}
	return strings.Join(names, ", ")
}

func hasMarker(root string, markers []marker) bool {
	for _, m := range markers {
		if body, err := os.ReadFile(filepath.Join(root, m.file)); err == nil && strings.Contains(string(body), m.needle) {
			return true
		}
	}
	return false
}

func ClientCommand(root string, fw Framework, env []string) ([]string, error) {
	if fw != Pytest {
		if err := checkRubyCore(root); err != nil {
			return nil, err
		}
		return []string{"bundle", "exec", "selective", string(fw)}, nil
	}
	client, ok := findPytestClient(root, env)
	if !ok {
		return nil, contract.Errorf(contract.RunnerMissing, "sel test could not find the selective pytest client; install selective-pytest in the project's virtualenv")
	}
	return []string{client, "pytest"}, nil
}

func checkRubyCore(root string) error {
	found := "none"
	lock, _ := os.ReadFile(filepath.Join(root, "Gemfile.lock"))
	if m := rubyCoreSpec.FindStringSubmatch(string(lock)); m != nil {
		found = strings.Join(m[1:], ".")
		if !olderThan(m[1:], minRubyCore) {
			return nil
		}
	}
	return contract.Errorf(contract.RunnerMissing, "sel test needs selective-ruby-core %s or newer (Gemfile.lock has %s)", minRubyCore, found)
}

func olderThan(version []string, minimum string) bool {
	for i, want := range strings.Split(minimum, ".") {
		have, _ := strconv.Atoi(version[i])
		need, _ := strconv.Atoi(want)
		if have != need {
			return have < need
		}
	}
	return false
}

func findPytestClient(root string, env []string) (string, bool) {
	var dirs []string
	if venv := envValue(env, "VIRTUAL_ENV"); venv != "" {
		dirs = append(dirs, filepath.Join(venv, "bin"))
	}
	dirs = append(dirs, filepath.Join(root, ".venv", "bin"), filepath.Join(root, "venv", "bin"))
	dirs = append(dirs, filepath.SplitList(envValue(env, "PATH"))...)
	for _, dir := range dirs {
		path := filepath.Join(dir, "selective")
		if script, err := os.ReadFile(path); err == nil && strings.Contains(string(script), "selective_pytest") {
			return path, true
		}
	}
	return "", false
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], key+"="); ok {
			return value
		}
	}
	return ""
}
