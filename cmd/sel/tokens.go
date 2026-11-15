package main

import (
	"fmt"
	"io"
	"strings"
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
		Short: "Inspect the API token in use",
		Long: `Read API tokens.

  sel tokens show  The token this command line is using`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(tokensShowCmd())
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
