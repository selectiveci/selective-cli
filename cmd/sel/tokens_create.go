package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

type createOptions struct {
	name         string
	agent        bool
	org          string
	repos        []string
	scopes       []string
	expiresIn    string
	dryRun       bool
	toStdout     bool
	output       string
	githubSecret string
	secretRepo   string
	json         bool
}

func tokensCreateCmd() *cobra.Command {
	var f setupFlags
	var opts createOptions
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an API token",
		Long: `Create an API token and put its secret in the place you name. The secret is
shown once and appears nowhere else, so exactly one of these is required
(--dry-run needs none, because it returns no secret):

  --stdout                       the secret on stdout; stdout must be a pipe
  --output PATH                  a new file, mode 0600; an existing file is refused
  --github-secret NAME           a GitHub Actions secret, set through gh;
    --secret-repo owner/name     the repository that gets it

--repo limits the token to those repositories and needs --org. --expires-in takes
a number of days (90d) or a Go duration (12h); the server caps it at 365 days.
A name that is already in use exits 5 (name_taken). Agent tokens cannot create
tokens: the server answers 403 (exit 6).

If the secret cannot be delivered after the token was created, the error names
the token's id and prefix and exits 9, so you can revoke it.`,
		Example: "  sel tokens create --name ci --agent --org acme --repo acme/web --scope read --expires-in 90d --output ci.token\n" +
			"  sel tokens create --name ci --stdout | pass insert -e selective/ci",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"exit_codes": "0,4,5,6,9", "mutates": "true", "secret_flags": "stdout"},
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := tokensEnv(cmd, &f)
			if err != nil {
				return err
			}
			opts.org, opts.json = f.org, f.json
			return tokensCreate(env, opts)
		},
	}
	f.bind(cmd, false)
	flags := cmd.Flags()
	flags.StringVar(&opts.name, "name", "", "Token name (required)")
	flags.BoolVar(&opts.agent, "agent", false, "Create an agent token (default: a user token)")
	flags.StringVar(&f.org, "org", "", "Limit the token to this organization slug")
	flags.StringArrayVar(&opts.repos, "repo", nil, "Limit the token to owner/name (repeatable; needs --org)")
	flags.StringSliceVar(&opts.scopes, "scope", nil, "read or write (repeatable)")
	flags.StringVar(&opts.expiresIn, "expires-in", "", "Lifetime, such as 90d or 12h")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "Validate the request and create nothing")
	flags.BoolVar(&opts.toStdout, "stdout", false, "Write the secret to stdout (a pipe only)")
	flags.StringVar(&opts.output, "output", "", "Write the secret to a new file, mode 0600")
	flags.StringVar(&opts.githubSecret, "github-secret", "", "Set this GitHub Actions secret to the token")
	flags.StringVar(&opts.secretRepo, "secret-repo", "", "Repository for --github-secret, owner/name")
	return cmd
}

func tokensCreate(env *setup.Env, o createOptions) error {
	req, err := o.request()
	if err != nil {
		return err
	}
	if err := o.checkSink(env.StdoutIsTerminal); err != nil {
		return err
	}
	minted, err := env.Client.MintToken(req, o.dryRun)
	if err != nil {
		return err
	}
	if !o.dryRun {
		if err := o.deliver(env, minted.Plaintext); err != nil {
			return contract.Errorf(contract.InternalError,
				"token %q (id %v, prefix %v) was created but its secret could not be delivered: %v; revoke it with: sel tokens revoke %v --yes",
				req.Name, minted.Token["id"], minted.Token["prefix"], err, minted.Token["id"])
		}
	}
	if env.JSON && !o.toStdout {
		return format.Write(env.Stdout, "token", minted.Token, minted.Result, minted.DryRun)
	}
	fmt.Fprintln(env.Stderr, o.sentence(minted))
	return nil
}

func (o createOptions) request() (api.MintRequest, error) {
	if strings.TrimSpace(o.name) == "" {
		return api.MintRequest{}, contract.Errorf(contract.UsageError, "--name is required")
	}
	seconds, err := parseExpiresIn(o.expiresIn)
	if err != nil {
		return api.MintRequest{}, err
	}
	req := api.MintRequest{Name: o.name, Scopes: o.scopes, Organization: o.org, Repos: o.repos, ExpiresInS: seconds}
	if o.agent {
		req.Kind = "agent"
	}
	return req, nil
}

func parseExpiresIn(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	var seconds int64
	var err error
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int64
		n, err = strconv.ParseInt(days, 10, 32)
		seconds = n * 86400
	} else {
		var d time.Duration
		d, err = time.ParseDuration(s)
		if d%time.Second == 0 {
			seconds = int64(d / time.Second)
		}
	}
	if err != nil || seconds <= 0 {
		return 0, contract.Errorf(contract.UsageError, "--expires-in %q is not a positive duration like 90d or 12h", s)
	}
	return seconds, nil
}

func (o createOptions) checkSink(stdoutIsTerminal bool) error {
	chosen := 0
	for _, set := range []bool{o.toStdout, o.output != "", o.githubSecret != ""} {
		if set {
			chosen++
		}
	}
	switch {
	case chosen > 1:
		return contract.Errorf(contract.UsageError, "choose one place for the secret: --stdout, --output or --github-secret")
	case chosen == 0 && !o.dryRun:
		return contract.Errorf(contract.UsageError, "say where the secret goes: --stdout (piped), --output PATH or --github-secret NAME --secret-repo owner/name")
	case o.toStdout && o.json:
		return contract.Errorf(contract.UsageError, "--stdout prints the secret, so it can't be combined with --json")
	case o.toStdout && stdoutIsTerminal:
		return contract.Errorf(contract.UsageError, "refusing to print a token to a terminal; pipe --stdout into a secret store")
	case o.githubSecret != "" && o.secretRepo == "":
		return contract.Errorf(contract.UsageError, "--github-secret needs --secret-repo owner/name (--repo limits the token, not where the secret goes)")
	}
	if o.output != "" {
		if _, err := os.Lstat(o.output); err == nil {
			return contract.Errorf(contract.UsageError, "%s already exists; remove it or choose another --output path", o.output)
		}
	}
	return nil
}

func (o createOptions) deliver(env *setup.Env, secret string) error {
	switch {
	case secret == "":
		return contract.Errorf(contract.InternalError, "the server returned no secret")
	case o.toStdout:
		_, err := io.WriteString(env.Stdout, secret+"\n")
		return err
	case o.output != "":
		return format.WriteSecretFile(o.output, []byte(secret+"\n"))
	}
	return env.RunGh(secret, "secret", "set", o.githubSecret, "--repo", o.secretRepo)
}

func (o createOptions) sentence(m *api.MintedToken) string {
	kind, _ := m.Token["kind"].(string)
	if m.DryRun {
		return fmt.Sprintf("Dry run: would create %s token %q.", kind, o.name)
	}
	where := "stdout"
	switch {
	case o.output != "":
		where = o.output
	case o.githubSecret != "":
		where = fmt.Sprintf("GitHub Actions secret %s on %s", o.githubSecret, o.secretRepo)
	}
	return fmt.Sprintf("Created %s token %q (%v); its secret went to %s.", kind, o.name, m.Token["prefix"], where)
}
