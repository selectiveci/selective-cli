package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"app.selective.ci":                 "https://app.selective.ci",
		"https://app.selective.ci/":        "https://app.selective.ci",
		"https://app.selective.ci/foo/bar": "https://app.selective.ci",
		"localhost:4000":                   "http://localhost:4000",
		"127.0.0.1:4100":                   "http://127.0.0.1:4100",
		"http://selective.test:8080":       "http://selective.test:8080",
		"  staging.selective.ci  ":         "https://staging.selective.ci",
	}
	for in, want := range cases {
		got, err := NormalizeHost(in)
		if err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x.y", "https://"} {
		if _, err := NormalizeHost(bad); err == nil {
			t.Errorf("NormalizeHost(%q) should fail", bad)
		}
	}
}

func TestLegacyConfigMigrates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SELECTIVE_CONFIG_DIR", dir)
	legacy := `{"token": "sk_live_old", "base_url": "http://localhost:4000"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentHost != "http://localhost:4000" || cfg.Hosts["http://localhost:4000"].Token != "sk_live_old" {
		t.Fatalf("legacy config not migrated: %+v", cfg)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if string(data) == legacy {
		t.Fatal("saved config kept the legacy shape")
	}
	info, _ := os.Stat(filepath.Join(dir, "config.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestResolveHostPrecedence(t *testing.T) {
	cfg := &Config{CurrentHost: "https://current.example"}
	t.Setenv("SELECTIVE_HOST", "")
	if h, _ := cfg.ResolveHost(""); h != "https://current.example" {
		t.Errorf("current host not used: %s", h)
	}
	t.Setenv("SELECTIVE_HOST", "env.example")
	if h, _ := cfg.ResolveHost(""); h != "https://env.example" {
		t.Errorf("SELECTIVE_HOST not preferred: %s", h)
	}
	if h, _ := cfg.ResolveHost("flag.example"); h != "https://flag.example" {
		t.Errorf("flag not preferred: %s", h)
	}
	t.Setenv("SELECTIVE_HOST", "")
	if h, _ := (&Config{}).ResolveHost(""); h != DefaultHost {
		t.Errorf("default not used: %s", h)
	}
}
