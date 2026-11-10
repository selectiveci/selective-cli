// Package config reads and writes ~/.config/selective/config.json.
//
// One CLI can be logged in to several Selective servers (production,
// staging, a local dev server), each keyed by its base URL. Tokens live in
// the OS keychain; the config file only holds one when no keychain is
// available (containers, headless Linux), and then it is 0600.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultHost is used when neither --hostname, SELECTIVE_HOST nor a
// current host is set.
const DefaultHost = "https://app.selective.ci"

// Host is what's remembered about one server.
type Host struct {
	User string `json:"user,omitempty"`
	// Token is only set when the keychain was unavailable at login.
	Token string `json:"token,omitempty"`
}

type Config struct {
	CurrentHost string           `json:"current_host,omitempty"`
	Hosts       map[string]*Host `json:"hosts,omitempty"`

	// Pre-login format (`selective auth --token`): read, migrated, never written.
	LegacyToken   string `json:"token,omitempty"`
	LegacyBaseURL string `json:"base_url,omitempty"`
}

// Dir can be overridden in tests.
var Dir = func() (string, error) {
	if d := os.Getenv("SELECTIVE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "selective"), nil
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Path is the config file's location, for messages.
func Path() string {
	p, err := path()
	if err != nil {
		return "~/.config/selective/config.json"
	}
	return p
}

func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Hosts: map[string]*Host{}}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", p, err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]*Host{}
	}
	cfg.migrateLegacy()
	return cfg, nil
}

// migrateLegacy folds the old {"token", "base_url"} shape into Hosts.
func (c *Config) migrateLegacy() {
	if c.LegacyToken == "" && c.LegacyBaseURL == "" {
		return
	}
	host := DefaultHost
	if c.LegacyBaseURL != "" {
		if h, err := NormalizeHost(c.LegacyBaseURL); err == nil {
			host = h
		}
	}
	if _, exists := c.Hosts[host]; !exists && c.LegacyToken != "" {
		c.Hosts[host] = &Host{Token: c.LegacyToken}
	}
	if c.CurrentHost == "" {
		c.CurrentHost = host
	}
	c.LegacyToken, c.LegacyBaseURL = "", ""
}

func Save(cfg *Config) error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p, err := path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename so a crash never leaves a truncated config behind.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ResolveHost picks the server to talk to: an explicit flag, then
// SELECTIVE_HOST, then the host last logged in to, then production.
func (c *Config) ResolveHost(flag string) (string, error) {
	for _, candidate := range []string{flag, os.Getenv("SELECTIVE_HOST"), c.CurrentHost} {
		if candidate != "" {
			return NormalizeHost(candidate)
		}
	}
	return DefaultHost, nil
}

// NormalizeHost turns "app.selective.ci", "https://app.selective.ci/" or
// "localhost:4000" into a canonical base URL. Bare hosts get https, except
// loopback addresses, which get http (a local dev server).
func NormalizeHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty hostname")
	}
	if !strings.Contains(raw, "://") {
		scheme := "https"
		hostOnly := strings.Split(raw, "/")[0]
		name := strings.Split(hostOnly, ":")[0]
		if name == "localhost" || strings.HasPrefix(name, "127.") {
			scheme = "http"
		}
		raw = scheme + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid hostname %q", raw)
	}
	// The runner gems take SELECTIVE_HOST as a websocket URL.
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("hostname must be http or https, got %q", u.Scheme)
	}
	return u.Scheme + "://" + u.Host, nil
}
