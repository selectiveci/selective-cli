package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "doctor-ready-root")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Gemfile.lock"), []byte(rspecLock), 0o644); err != nil {
		panic(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "bundle"), nil, 0o755); err != nil {
		panic(err)
	}
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		panic(err)
	}
	if err := os.Chdir(root); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
