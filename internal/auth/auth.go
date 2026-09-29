// Package auth implements `selective auth login|status|logout`.
package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/browser"
	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/credentials"
	"github.com/selectiveci/selective-cli/internal/devicelogin"
)

type LoginOptions struct {
	Hostname  string
	WithToken bool // read a token from stdin instead of the browser flow
	NoBrowser bool
	Version   string
	Stdin     io.Reader
	Stderr    io.Writer
}

// Login signs the CLI in to a host, by default through the browser.
func Login(opts LoginOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("could not load config: %w", err)
	}
	host, err := cfg.ResolveHost(opts.Hostname)
	if err != nil {
		return err
	}
	if os.Getenv("SELECTIVE_TOKEN") != "" {
		fmt.Fprintln(opts.Stderr, "! SELECTIVE_TOKEN is set; it takes precedence over the login you're about to store.")
	}

	var token, email string
	if opts.WithToken {
		token, err = readToken(opts.Stdin)
		if err != nil {
			return err
		}
		me, err := api.New(host, token).Me()
		if errors.Is(err, api.ErrUnauthorized) {
			return fmt.Errorf("that token was rejected by %s", host)
		}
		if err != nil {
			return err
		}
		email = me.Email
	} else {
		grant, err := browserLogin(host, opts)
		if err != nil {
			return err
		}
		token, email = grant.AccessToken, grant.User.Email
	}

	source, err := credentials.Store(cfg, host, email, token)
	if err != nil {
		return fmt.Errorf("logged in, but could not save the token: %w", err)
	}
	fmt.Fprintf(opts.Stderr, "✓ Logged in to %s as %s\n", host, email)
	if source == credentials.SourceFile {
		fmt.Fprintf(opts.Stderr, "! No system keychain is available; the token is stored in %s (mode 0600).\n", config.Path())
	}
	return nil
}

func browserLogin(host string, opts LoginOptions) (*devicelogin.Grant, error) {
	client := devicelogin.New(host)
	code, err := client.Start(clientName(opts.Version))
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(opts.Stderr, "! First copy your one-time code: %s\n", code.UserCode)
	opened := !opts.NoBrowser && isatty.IsTerminal(os.Stderr.Fd()) && browser.Open(code.VerificationURIComplete)
	if opened {
		fmt.Fprintf(opts.Stderr, "Opened %s in your browser.\n", code.VerificationURIComplete)
	} else {
		fmt.Fprintf(opts.Stderr, "Open this URL in a browser where you're signed in to Selective:\n  %s\n", code.VerificationURIComplete)
	}
	fmt.Fprintln(opts.Stderr, "Waiting for you to authorize the CLI… (Ctrl+C to cancel)")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	grant, err := client.Wait(ctx, code)
	if errors.Is(err, context.Canceled) {
		return nil, errors.New("login cancelled")
	}
	return grant, err
}

func clientName(version string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown host"
	}
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("selective-cli %s on %s", version, host)
}

// Tokens come in on stdin (`--with-token < file`, or piped from a secret
// store), never as an argument: arguments land in shell history and `ps`.
func readToken(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errors.New("no token on stdin (usage: selective auth login --with-token < token.txt)")
	}
	if !strings.HasPrefix(token, "sk_live_") {
		return "", errors.New("that doesn't look like a Selective API token (they start with sk_live_)")
	}
	return token, nil
}

// Status reports the login for a host and checks the token still works.
// Returns a non-nil error (exit 1) when not logged in or the token is bad.
func Status(hostname string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	host, err := cfg.ResolveHost(hostname)
	if err != nil {
		return err
	}
	cred, err := credentials.Lookup(cfg, host)
	if err != nil {
		fmt.Fprintf(stdout, "%s\n  ✗ Not logged in. Run: selective auth login", host)
		if host != config.DefaultHost {
			fmt.Fprintf(stdout, " --hostname %s", host)
		}
		fmt.Fprintln(stdout)
		return errSilent
	}

	me, err := api.New(host, cred.Token).Me()
	switch {
	case errors.Is(err, api.ErrUnauthorized):
		fmt.Fprintf(stdout, "%s\n  ✗ The stored token was rejected (revoked?). Run: selective auth login\n", host)
		return errSilent
	case err != nil:
		return err
	}
	fmt.Fprintf(stdout, "%s\n  ✓ Logged in as %s\n  Organizations: %s\n  Token: %s (from %s)\n",
		host, me.Email, strings.Join(me.OrgSlugs, ", "), mask(cred.Token), cred.Source)
	return nil
}

// Logout revokes the token on the server, then forgets it locally.
func Logout(hostname string, stderr io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	host, err := cfg.ResolveHost(hostname)
	if err != nil {
		return err
	}
	if os.Getenv("SELECTIVE_TOKEN") != "" {
		return errors.New("SELECTIVE_TOKEN is set; unset it (logout doesn't revoke tokens it didn't store)")
	}
	cred, err := credentials.Lookup(cfg, host)
	if err != nil {
		fmt.Fprintf(stderr, "Not logged in to %s.\n", host)
		return nil
	}

	revokeErr := api.New(host, cred.Token).RevokeCurrent()
	if err := credentials.Forget(cfg, host); err != nil {
		return err
	}
	switch {
	case revokeErr == nil, errors.Is(revokeErr, api.ErrUnauthorized):
		fmt.Fprintf(stderr, "✓ Logged out of %s and revoked the token.\n", host)
		return nil
	default:
		fmt.Fprintf(stderr, "✓ Logged out of %s locally, but couldn't revoke the token on the server (%v).\n  Revoke it on your API tokens page.\n", host, revokeErr)
		return nil
	}
}

func mask(token string) string {
	if len(token) <= 14 {
		return "sk_live_…"
	}
	return token[:14] + "…"
}

// errSilent signals failure (exit 1) after the message has been printed.
var errSilent = errors.New("")

// IsSilent reports whether err was already explained to the user.
func IsSilent(err error) bool { return errors.Is(err, errSilent) }

// LegacyTokenLogin supports `selective auth --token sk_live_…` from before
// `auth login` existed.
func LegacyTokenLogin(token, hostname, version string, stderr io.Writer) error {
	fmt.Fprintln(stderr, "! `selective auth --token` is deprecated: use `selective auth login`, or `selective auth login --with-token < file` for scripts.")
	return Login(LoginOptions{
		Hostname:  hostname,
		WithToken: true,
		Version:   version,
		Stdin:     strings.NewReader(token + "\n"),
		Stderr:    stderr,
	})
}
