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
	if err := os.Chdir(root); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
