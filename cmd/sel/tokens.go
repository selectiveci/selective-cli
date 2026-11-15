package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

var tokenReadAnnotations = map[string]string{"exit_codes": "0,3,4,5,6"}

func tokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "List your API tokens and inspect the one in use",
		Long: `Read API tokens.

  sel tokens list  Your tokens, with the scope of each
  sel tokens show  The token this command line is using`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(tokensListCmd(), tokensShowCmd())
	return cmd
}

func tokensListCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the tokens you can see",
		Long: `List the API tokens this login can see, with each one's kind, scopes,
organization, repositories and expiry. Pages are fetched until next_cursor is
null; the walk stops early after 20000 tokens or 2000 requests, and notices then
carries pagination_stopped_early.

On a terminal, an organization or repositories column of "all" means the token
is not limited, and an expiry of "never" means it does not expire. Token
secrets are never shown.

Agent tokens cannot list tokens: the server answers 403 (exit 6).`,
		Example:     "  sel tokens list\n  sel tokens list --json",
		Args:        cobra.NoArgs,
		Annotations: tokenReadAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := tokensEnv(cmd, &f)
			if err != nil {
				return err
			}
			return tokensList(env)
		},
	}
	f.bind(cmd, false)
	return cmd
}

func tokensShowCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:         "show",
		Short:       "Show the token this command line is using",
		Long:        "Show the token this command line is using. For any other token, see sel tokens list.",
		Example:     "  sel tokens show --json",
		Args:        cobra.NoArgs,
		Annotations: tokenReadAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, err := tokensEnv(cmd, &f)
			if err != nil {
				return err
			}
			return tokensShow(env)
		},
	}
	f.bind(cmd, false)
	return cmd
}

func tokensEnv(cmd *cobra.Command, f *setupFlags) (*setup.Env, error) {
	env, _, err := f.env(false)
	if err != nil {
		return nil, err
	}
	env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	return env, nil
}

func tokensList(env *setup.Env) error {
	page, err := env.Client.Tokens()
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, page)
	}
	if len(page.Tokens) == 0 {
		fmt.Fprintln(env.Stderr, "No tokens.")
		return nil
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPREFIX\tKIND\tSCOPES\tORG\tREPOS\tEXPIRES (UTC)")
	for _, t := range page.Tokens {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Prefix, t.Kind, strings.Join(t.Scopes, ","), orAll(t.Organization), repoList(t.Repos), expiry(t.ExpiresAt))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if page.NextCursor != nil {
		fmt.Fprintf(env.Stderr, "Notice: stopped early after %d tokens; more exist on the server\n", len(page.Tokens))
	}
	return nil
}

func tokensShow(env *setup.Env) error {
	token, err := env.Client.CurrentToken()
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Resource(env.Stdout, "token", token)
	}
	renderToken(env.Stdout, token)
	return nil
}

func renderToken(w io.Writer, t *api.Token) {
	fmt.Fprintf(w, "%s\n\n", t.Name)
	fmt.Fprintf(w, "ID:        %s\nPrefix:    %s\nKind:      %s\nScopes:    %s\n", t.ID, t.Prefix, t.Kind, strings.Join(t.Scopes, ","))
	fmt.Fprintf(w, "Org:       %s\nRepos:     %s\nExpires:   %s\n", orAll(t.Organization), repoList(t.Repos), expiry(t.ExpiresAt))
	fmt.Fprintf(w, "Source:    %s\nCreated:   %s UTC\n", t.Source, utcMinute(t.InsertedAt))
}

func orAll(s *string) string {
	if s == nil {
		return "all"
	}
	return *s
}

func repoList(repos []string) string {
	if repos == nil {
		return "all"
	}
	return strings.Join(repos, ",")
}

func expiry(at *string) string {
	if at == nil {
		return "never"
	}
	return utcMinute(*at)
}

func utcMinute(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.UTC().Format("2006-01-02 15:04")
}
