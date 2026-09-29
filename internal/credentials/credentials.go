// Package credentials finds, stores and forgets the API token for a host.
//
// Precedence when reading: SELECTIVE_TOKEN (CI and agents; never written
// anywhere), then the OS keychain, then the config file. Writing prefers the
// keychain and falls back to the 0600 config file only when there is no
// keychain to talk to (containers, headless Linux without Secret Service).
package credentials

import (
	"errors"
	"os"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/zalando/go-keyring"
)

const keyringService = "selective-cli"

// Source says where a token came from, for `auth status`.
type Source string

const (
	SourceEnv     Source = "SELECTIVE_TOKEN"
	SourceKeyring Source = "keychain"
	SourceFile    Source = "config file"
)

type Credential struct {
	Host   string
	Token  string
	Source Source
}

// ErrNotLoggedIn means there is no token for the host.
var ErrNotLoggedIn = errors.New("not logged in")

// Lookup returns the token to use for host.
func Lookup(cfg *config.Config, host string) (*Credential, error) {
	if t := os.Getenv("SELECTIVE_TOKEN"); t != "" {
		return &Credential{Host: host, Token: t, Source: SourceEnv}, nil
	}
	if t, err := keyring.Get(keyringService, host); err == nil && t != "" {
		return &Credential{Host: host, Token: t, Source: SourceKeyring}, nil
	}
	if h := cfg.Hosts[host]; h != nil && h.Token != "" {
		return &Credential{Host: host, Token: h.Token, Source: SourceFile}, nil
	}
	return nil, ErrNotLoggedIn
}

// Store saves token for host and makes host current. Returns where it went.
func Store(cfg *config.Config, host, user, token string) (Source, error) {
	h := cfg.Hosts[host]
	if h == nil {
		h = &config.Host{}
		cfg.Hosts[host] = h
	}
	h.User = user

	source := SourceKeyring
	if err := keyring.Set(keyringService, host, token); err != nil {
		h.Token = token
		source = SourceFile
	} else {
		h.Token = ""
	}
	cfg.CurrentHost = host
	return source, config.Save(cfg)
}

// Forget removes everything stored for host.
func Forget(cfg *config.Config, host string) error {
	_ = keyring.Delete(keyringService, host)
	delete(cfg.Hosts, host)
	if cfg.CurrentHost == host {
		cfg.CurrentHost = ""
		for other := range cfg.Hosts {
			cfg.CurrentHost = other
			break
		}
	}
	return config.Save(cfg)
}
