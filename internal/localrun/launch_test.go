package localrun

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const platformLock = `GEM
  remote: https://rubygems.org/
  specs:
    logger (1.7.0)
    selective-ruby-core (0.3.0-x86_64-linux)
      logger (~> 1.6)
      zeitwerk (~> 2.6.12)
    zeitwerk (2.6.18)

PLATFORMS
  x86_64-linux

DEPENDENCIES
  selective-ruby-core (= 0.3.0)

CHECKSUMS
  selective-ruby-core (0.3.0-x86_64-linux)
`

const dependencyOnlyLock = `GEM
  specs:
    selective-ruby-rspec (0.1.11)
      selective-ruby-core (>= 0.2.3)
`

func lockWithCore(version string) string {
	return "GEM\n  specs:\n    selective-ruby-core (" + version + ")\n      logger (~> 1.6)\n"
}

func project(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDetectFramework(t *testing.T) {
	rails := "    rspec-core (3.13.7)\n    minitest (5.25.4)\n    rails (8.0.1)\n"
	cases := []struct {
		name     string
		files    map[string]string
		explicit string
		want     Framework
		code     contract.Code
		message  string
	}{
		{name: "rspec in the lockfile", files: map[string]string{"Gemfile.lock": rails}, want: RSpec},
		{name: "rspec in the Gemfile", files: map[string]string{"Gemfile": "gem \"rspec-core\"\n"}, want: RSpec},
		{name: "minitest is keyed on the selective client, not the minitest gem", files: map[string]string{"Gemfile.lock": "    minitest (5.25.4)\n"}, code: contract.NoFramework},
		{name: "minitest with its client", files: map[string]string{"Gemfile.lock": "    minitest (5.25.4)\n    selective-ruby-minitest (0.1.5)\n"}, want: Minitest},
		{name: "pytest in pyproject.toml", files: map[string]string{"pyproject.toml": "[tool.pytest.ini_options]\n"}, want: Pytest},
		{name: "an empty pytest.ini still means pytest", files: map[string]string{"pytest.ini": ""}, want: Pytest},
		{name: "pytest in setup.cfg", files: map[string]string{"setup.cfg": "[tool:pytest]\n"}, want: Pytest},
		{name: "pytest in tox.ini", files: map[string]string{"tox.ini": "[pytest]\n"}, want: Pytest},
		{name: "a pyproject.toml without pytest", files: map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n"}, code: contract.NoFramework},
		{name: "nothing", files: map[string]string{"README.md": "hi"}, code: contract.NoFramework, message: "--framework"},
		{name: "rspec and pytest", files: map[string]string{"Gemfile.lock": rails, "pytest.ini": ""}, code: contract.NoFramework, message: "rspec, pytest"},
		{name: "explicit wins over ambiguity", files: map[string]string{"Gemfile.lock": rails, "pytest.ini": ""}, explicit: "pytest", want: Pytest},
		{name: "explicit is not checked against markers", files: map[string]string{}, explicit: "rspec", want: RSpec},
		{name: "explicit unknown", files: map[string]string{}, explicit: "jest", code: contract.UsageError, message: "rspec, minitest, pytest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectFramework(project(t, tc.files), tc.explicit)
			if codeOf(err) != tc.code || got != tc.want {
				t.Fatalf("got %q, %v; want %q with code %q", got, err, tc.want, tc.code)
			}
			if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
				t.Errorf("message %q lacks %q", err, tc.message)
			}
		})
	}
}

func TestClientCommandRefusesRubyCoreBelowZeroDotThree(t *testing.T) {
	cases := []struct {
		name  string
		lock  *string
		found string
	}{
		{"0.2.15", ptr(lockWithCore("0.2.15")), "0.2.15"},
		{"0.2.99", ptr(lockWithCore("0.2.99")), "0.2.99"},
		{"no Gemfile.lock", nil, "none"},
		{"a lockfile that never resolved selective-ruby-core", ptr("GEM\n  specs:\n    rspec-core (3.13.7)\n"), "none"},
		{"only a dependency constraint on it", ptr(dependencyOnlyLock), "none"},
	}
	for _, fw := range []Framework{RSpec, Minitest} {
		for _, tc := range cases {
			t.Run(string(fw)+" "+tc.name, func(t *testing.T) {
				files := map[string]string{}
				if tc.lock != nil {
					files["Gemfile.lock"] = *tc.lock
				}
				_, err := ClientCommand(project(t, files), fw, nil)
				if codeOf(err) != contract.RunnerMissing {
					t.Fatalf("code %q (%v), want runner_missing", codeOf(err), err)
				}
				if !strings.Contains(err.Error(), "sel test needs selective-ruby-core 0.3.0 or newer") || !strings.Contains(err.Error(), tc.found) {
					t.Errorf("message %q must name 0.3.0 and what was found (%s)", err, tc.found)
				}
			})
		}
	}
}

func TestClientCommandAcceptsRubyCoreFromZeroDotThree(t *testing.T) {
	for name, lock := range map[string]string{
		"0.3.0": lockWithCore("0.3.0"), "0.10.0 is newer than 0.3.0": lockWithCore("0.10.0"),
		"1.0.0": lockWithCore("1.0.0"), "a platform gem": platformLock,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ClientCommand(project(t, map[string]string{"Gemfile.lock": lock}), RSpec, nil)
			if want := []string{"bundle", "exec", "selective", "rspec"}; err != nil || !slices.Equal(got, want) {
				t.Errorf("got %v, %v; want %v", got, err, want)
			}
		})
	}
	got, _ := ClientCommand(project(t, map[string]string{"Gemfile.lock": lockWithCore("0.3.0")}), Minitest, nil)
	if want := []string{"bundle", "exec", "selective", "minitest"}; !slices.Equal(got, want) {
		t.Errorf("minitest got %v, want %v", got, want)
	}
}

const pytestScript = "#!/x/python\nfrom selective_pytest.cli import main\n"
const rubyScript = "#!/x/ruby\nload Gem.bin_path('selective-ruby-core', 'selective')\n"

func ptr(s string) *string { return &s }

func TestClientCommandResolvesThePytestClientAndSkipsTheRubyOneWithTheSameName(t *testing.T) {
	root := project(t, map[string]string{
		"active/bin/selective": pytestScript, ".venv/bin/selective": pytestScript, "venv/bin/selective": pytestScript,
		"first/selective": rubyScript, "second/selective": pytestScript, "only-ruby/selective": rubyScript,
	})
	at := func(rel string) string { return filepath.Join(root, rel) }
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"the active virtualenv", []string{"VIRTUAL_ENV=" + at("active"), "PATH=" + at("second")}, at("active/bin/selective")},
		{"the project .venv", []string{"PATH=" + at("second")}, at(".venv/bin/selective")},
		{"the first pytest client on PATH", []string{"PATH=" + at("first") + string(os.PathListSeparator) + at("second")}, at("second/selective")},
		{"no pytest client on PATH", []string{"PATH=" + at("only-ruby")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := root
			if tc.name != "the project .venv" {
				dir = project(t, nil)
			}
			got, err := ClientCommand(dir, Pytest, tc.env)
			if tc.want == "" {
				if codeOf(err) != contract.RunnerMissing || !strings.Contains(err.Error(), "selective-pytest") {
					t.Fatalf("got %v, %v; want runner_missing naming selective-pytest", got, err)
				}
				return
			}
			if want := []string{tc.want, "pytest"}; err != nil || !slices.Equal(got, want) {
				t.Fatalf("got %v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestClientCommandFindsTheProjectVenvDirectory(t *testing.T) {
	root := project(t, map[string]string{"venv/bin/selective": pytestScript})
	got, err := ClientCommand(root, Pytest, nil)
	if want := []string{filepath.Join(root, "venv", "bin", "selective"), "pytest"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
}
