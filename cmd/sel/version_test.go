package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/update"
)

func versionAs(t *testing.T, build string, cache string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SELECTIVE_CONFIG_DIR", dir)
	if cache != "" {
		if err := os.WriteFile(filepath.Join(dir, "update-check.json"), []byte(cache), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previous := version
	version = build
	t.Cleanup(func() { version = previous })
	code, stdout, _ := execute(append([]string{"version"}, args...)...)
	return code, stdout
}

func versionDocument(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var doc struct {
		V       int            `json:"v"`
		Version map[string]any `json:"version"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.V != 1 || !strings.HasSuffix(stdout, "}\n") || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("not one {\"v\":1,\"version\":{...}} line: %v\n%s", err, stdout)
	}
	return doc.Version
}

func TestVersionJSONReportsWhatIsKnown(t *testing.T) {
	cache := `{"latest_version":"0.3.0","checked_at":"2026-11-13T08:00:00Z","server_min_version":"0.2.0"}`

	code, stdout := versionAs(t, "0.1.0", cache, "--json")

	got := versionDocument(t, stdout)
	want := map[string]any{
		"version":          "0.1.0",
		"commit":           commit,
		"update_available": true,
		"latest_version":   "0.3.0",
		"min_version":      "0.2.0",
		"below_minimum":    true,
		"url":              "https://github.com/selectiveci/selective-cli/releases/tag/v0.3.0",
		"checked_at":       "2026-11-13T08:00:00Z",
		"upgrade_command":  update.UpgradeCommand,
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 even when below the minimum", code)
	}
	if len(got) != len(want) {
		t.Errorf("fields = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
}

func TestVersionJSONOmitsWhatIsUnknown(t *testing.T) {
	cases := map[string]struct{ build, cache string }{
		"never checked":        {"0.1.0", ""},
		"dev build with cache": {"dev", `{"latest_version":"0.3.0"}`},
		"garbage cache":        {"0.1.0", `{"latest_version":"soon"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout := versionAs(t, tc.build, tc.cache, "--json")

			got := versionDocument(t, stdout)
			if code != 0 || len(got) != 2 || got["version"] != tc.build || got["commit"] != commit {
				t.Errorf("exit %d, fields %v; want only version and commit", code, got)
			}
		})
	}
}

func TestVersionJSONSaysUpToDateWhenLatestIsKnownAndNotNewer(t *testing.T) {
	_, stdout := versionAs(t, "0.3.0", `{"latest_version":"0.3.0"}`, "--json")

	got := versionDocument(t, stdout)
	if got["update_available"] != false || got["upgrade_command"] != nil || got["below_minimum"] != nil {
		t.Errorf("fields = %v, want update_available:false and no upgrade_command or below_minimum", got)
	}
}

func TestPlainVersionKeepsItsFirstLine(t *testing.T) {
	known := plainVersion(update.Info{Version: "0.1.0", Commit: "abc1234", LatestVersion: "0.3.0"})
	unknown := plainVersion(update.Info{Version: "0.1.0", Commit: "abc1234"})

	if known != "sel 0.1.0 (abc1234)\nlatest: 0.3.0\n" || unknown != "sel 0.1.0 (abc1234)\n" {
		t.Errorf("known %q, unknown %q", known, unknown)
	}
}
