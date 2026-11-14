package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/zalando/go-keyring"
)

const token = "sk_doctor_secret_7f3a9c"

func isolate(t *testing.T, tok string) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	t.Setenv("SELECTIVE_TOKEN", tok)
	t.Setenv("SELECTIVE_HOST", "")
	t.Setenv("SELECTIVE_ORG", "")
}

func run(t *testing.T, host string, opts Options) *Report {
	t.Helper()
	isolate(t, token)
	opts.Hostname = host
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	return Run(opts)
}

func shape(r *Report) string {
	parts := make([]string, len(r.Checks))
	for i, c := range r.Checks {
		parts[i] = c.Name + "=" + string(c.Status)
	}
	return strings.Join(parts, " ")
}

func check(t *testing.T, r *Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %s", name, shape(r))
	return Check{}
}

func exitOf(r *Report) int {
	c, ok := r.Err().(contract.Coded)
	if !ok {
		return 0
	}
	return c.ExitCode()
}

func TestInvalidConfigIsAUsageErrorNamingThePath(t *testing.T) {
	isolate(t, token)
	if err := writeFile(config.Path(), "{not json"); err != nil {
		t.Fatal(err)
	}

	report := Run(Options{Timeout: time.Minute})

	cfg := check(t, report, "config")
	if cfg.Status != Fail || cfg.Code != contract.UsageError || !strings.Contains(cfg.Fix, config.Path()) {
		t.Errorf("config = %+v", cfg)
	}
	if exitOf(report) != 5 {
		t.Errorf("exit = %d, want 5", exitOf(report))
	}
}

func TestBadHostnameIsAUsageError(t *testing.T) {
	report := run(t, "ftp://nope", Options{})

	if got := check(t, report, "config"); got.Status != Fail || got.Code != contract.UsageError {
		t.Errorf("config = %+v", got)
	}
}

func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}
