package credentials

import (
	"errors"
	"testing"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/zalando/go-keyring"
)

const host = "https://app.selective.ci"

func setup(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	t.Setenv("SELECTIVE_TOKEN", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestStoresInTheKeychainWhenThereIsOne(t *testing.T) {
	keyring.MockInit()
	cfg := setup(t)
	source, err := Store(cfg, host, "ben@example.com", "sk_live_abc")
	if err != nil || source != SourceKeyring {
		t.Fatalf("Store = %v, %v", source, err)
	}
	if cfg.Hosts[host].Token != "" {
		t.Fatal("token was also written to the config file")
	}
	reloaded, _ := config.Load()
	cred, err := Lookup(reloaded, host)
	if err != nil || cred.Token != "sk_live_abc" || cred.Source != SourceKeyring {
		t.Fatalf("Lookup = %+v, %v", cred, err)
	}
}

func TestFallsBackToTheConfigFileWithoutAKeychain(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	cfg := setup(t)
	source, err := Store(cfg, host, "ben@example.com", "sk_live_abc")
	if err != nil || source != SourceFile {
		t.Fatalf("Store = %v, %v", source, err)
	}
	reloaded, _ := config.Load()
	cred, err := Lookup(reloaded, host)
	if err != nil || cred.Source != SourceFile || cred.Token != "sk_live_abc" {
		t.Fatalf("Lookup = %+v, %v", cred, err)
	}
}

func TestEnvTokenWins(t *testing.T) {
	keyring.MockInit()
	cfg := setup(t)
	_, _ = Store(cfg, host, "ben@example.com", "sk_live_stored")
	t.Setenv("SELECTIVE_TOKEN", "sk_live_env")
	cred, _ := Lookup(cfg, host)
	if cred.Token != "sk_live_env" || cred.Source != SourceEnv {
		t.Fatalf("env token not preferred: %+v", cred)
	}
}

func TestForget(t *testing.T) {
	keyring.MockInit()
	cfg := setup(t)
	_, _ = Store(cfg, host, "ben@example.com", "sk_live_abc")
	_, _ = Store(cfg, "http://localhost:4000", "ben@example.com", "sk_live_dev")
	if err := Forget(cfg, "http://localhost:4000"); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(cfg, "http://localhost:4000"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("still logged in: %v", err)
	}
	if cfg.CurrentHost != host {
		t.Fatalf("current host = %q, want the remaining login", cfg.CurrentHost)
	}
}
