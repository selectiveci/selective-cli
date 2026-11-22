package update

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func nonRegularStatePaths(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{}
	for name, create := range map[string]func(string) error{
		"fifo with no writer":   func(p string) error { return syscall.Mkfifo(p, 0o600) },
		"fifo with writer held": func(p string) error { return syscall.Mkfifo(p, 0o600) },
		"directory":             func(p string) error { return os.Mkdir(p, 0o700) },
		"dangling symlink":      func(p string) error { return os.Symlink(filepath.Join(dir, "missing"), p) },
	} {
		path := filepath.Join(dir, name)
		if err := create(path); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	holder, err := os.OpenFile(paths["fifo with writer held"], os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	return paths
}

func inStateDir(t *testing.T, path string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SELECTIVE_CONFIG_DIR", dir)
	if err := os.Rename(path, filepath.Join(dir, cacheFile)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func returnsPromptly(t *testing.T, call func()) {
	t.Helper()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		call()
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("still blocked on the state file")
	}
}

func TestANonRegularStateFileLoadsAsNoState(t *testing.T) {
	for name, path := range nonRegularStatePaths(t) {
		t.Run(name, func(t *testing.T) {
			inStateDir(t, path)
			var st State
			returnsPromptly(t, func() { st = LoadState() })

			if st != (State{}) {
				t.Errorf("state = %+v, want none", st)
			}
		})
	}
}

func TestANonRegularStateFileIsLeftAloneByWrites(t *testing.T) {
	for name, path := range nonRegularStatePaths(t) {
		t.Run(name, func(t *testing.T) {
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			dir := inStateDir(t, path)
			previous := Version
			Version = "0.1.0"
			t.Cleanup(func() { Version = previous })

			returnsPromptly(t, func() { RecordServerHeaders(headers("0.3.0", "")) })

			after, err := os.Lstat(filepath.Join(dir, cacheFile))
			if err != nil || after.Mode().Type() != before.Mode().Type() {
				t.Errorf("state path is now %v (%v), was %v", after, err, before.Mode().Type())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Errorf("directory holds %d entries, want only the state path", len(entries))
			}
		})
	}
}

func TestASymlinkedStateFileIsReadButNeverReplaced(t *testing.T) {
	dir := isolated(t, "0.1.0")
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	original := `{"server_min_version":"0.2.0"}`
	if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, cacheFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	RecordServerHeaders(headers("0.3.0", ""))

	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v, %v", info, err)
	}
	if body, _ := os.ReadFile(target); string(body) != original {
		t.Errorf("target = %q, want it untouched", body)
	}
	if st := LoadState(); st.ServerMinVersion != "0.2.0" {
		t.Errorf("state = %+v, want the symlink target read", st)
	}
}
