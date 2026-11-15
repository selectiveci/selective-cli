package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type foldFS struct{ fstest.MapFS }

func (f foldFS) resolve(name string) string {
	if name == "." {
		return name
	}
	dir, spelled := ".", []string{}
	for _, part := range strings.Split(name, "/") {
		entries, _ := f.MapFS.ReadDir(dir)
		found := part
		for _, e := range entries {
			if e.Name() == part {
				found = part
				break
			}
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
			}
		}
		spelled = append(spelled, found)
		dir = strings.Join(spelled, "/")
	}
	return dir
}

func (f foldFS) Open(name string) (fs.File, error)     { return f.MapFS.Open(f.resolve(name)) }
func (f foldFS) Stat(name string) (fs.FileInfo, error) { return f.MapFS.Stat(f.resolve(name)) }
func (f foldFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return f.MapFS.ReadDir(f.resolve(name))
}

func TestOnDiskCaseSpellsEachPathPartAsTheDirectoryHasIt(t *testing.T) {
	disk := foldFS{fstest.MapFS{
		"tests/test_calc.py": {},
		"Docs/a.md":          {},
		"docs/b.md":          {},
	}}
	cases := map[string]string{
		"TESTS":               "tests",
		"Tests/Test_Calc.PY":  "tests/test_calc.py",
		"docs":                "docs",
		"Docs":                "Docs",
		"tests/missing/Thing": "tests/missing/Thing",
		".":                   ".",
	}
	for given, want := range cases {
		if got := onDiskCase(disk, given); got != want {
			t.Errorf("onDiskCase(%q) = %q, want %q", given, got, want)
		}
	}
}

func TestOnDiskCaseLeavesACaseSensitiveDiskAlone(t *testing.T) {
	disk := fstest.MapFS{"Tests/a.py": {}, "tests/b.py": {}}

	for _, given := range []string{"Tests", "tests", "tests/b.py"} {
		if got := onDiskCase(disk, given); got != given {
			t.Errorf("onDiskCase(%q) = %q, want it unchanged", given, got)
		}
	}
}

func TestRelativeToSpellsBothPathsAsTheDiskDoes(t *testing.T) {
	disk := foldFS{fstest.MapFS{"users/me/project/tests/test_calc.py": {}}}
	cases := []struct{ root, abs, want string }{
		{"/Users/me/project", "/users/me/project/tests", "tests"},
		{"/users/me/project", "/Users/Me/Project/Tests/test_calc.py", "tests/test_calc.py"},
		{"/Users/me/project", "/users/me/project", "."},
		{"/Users/me/project", "/users/me/elsewhere", "../elsewhere"},
	}
	for _, tc := range cases {
		if got, err := relativeTo(disk, tc.root, tc.abs); err != nil || got != tc.want {
			t.Errorf("relativeTo(%q, %q) = %q, %v, want %q", tc.root, tc.abs, got, err, tc.want)
		}
	}
}

func TestRelativeToResolvesSymlinksButKeepsTheNameOfAnInTreeLink(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "real", "project")
	for _, dir := range []string{filepath.Join(project, "tests"), filepath.Join(base, "real", "outside")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	links := map[string]string{
		filepath.Join(base, "link"):          filepath.Join(base, "real"),
		filepath.Join(project, "tests_link"): filepath.Join(project, "tests"),
		filepath.Join(project, "external"):   filepath.Join(base, "real", "outside"),
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
	}
	linked := filepath.Join(base, "link", "project")
	cases := []struct{ name, root, abs, want string }{
		{"the checkout reached through a link, the path by its physical name", linked, filepath.Join(project, "tests"), "tests"},
		{"the checkout by its physical name, the path through the link", project, filepath.Join(linked, "tests"), "tests"},
		{"a link inside the checkout keeps its own name", project, filepath.Join(project, "tests_link"), "tests_link"},
		{"a link that leaves the checkout resolves to where it points", project, filepath.Join(project, "external"), "../outside"},
	}
	for _, tc := range cases {
		if got, err := relativeTo(os.DirFS("/"), tc.root, tc.abs); err != nil || got != tc.want {
			t.Errorf("%s: relativeTo = %q, %v, want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestCandidatePathsRefuseWhatIsMissingOrOutsideTheDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := candidatePaths(root, []string{"tests"}); err != nil || len(got) != 1 || got[0] != "tests" {
		t.Errorf("candidatePaths(tests) = %v, %v, want [tests]", got, err)
	}
	for _, bad := range []string{"nope", "../elsewhere", filepath.Dir(root)} {
		_, err := candidatePaths(root, []string{bad})
		if coded, ok := err.(contract.Coded); !ok || coded.ErrorCode() != contract.UsageError {
			t.Errorf("candidatePaths(%q) err = %v, want usage_error", bad, err)
		}
	}
}
