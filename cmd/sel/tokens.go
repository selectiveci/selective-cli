package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

var tokenID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var tokenReadAnnotations = map[string]string{"exit_codes": "0,3,4,5,6"}

func tokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "List, inspect, create and revoke API tokens",
		Long: `Manage API tokens.

  sel tokens list    Your tokens, with the scope of each
  sel tokens show    The token this command line is using
  sel tokens create  Create a token; its secret goes to a sink you name
  sel tokens revoke  Revoke one of your tokens`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(tokensListCmd(), tokensShowCmd(), tokensCreateCmd(), tokensRevokeCmd())
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

type revokeOptions struct {
	dryRun   bool
	yes      bool
	ifExists bool
}

func tokensRevokeCmd() *cobra.Command {
	var f setupFlags
	var opts revokeOptions
	cmd := &cobra.Command{
		Use:   "revoke ID",
		Short: "Revoke one of your tokens",
		Long: `Revoke the token with this ID (sel tokens list shows the IDs). It stops
working at once and its name is free to mint again.

--yes is required to revoke, with or without a terminal. --dry-run reports what
would happen, changes nothing and needs no --yes.

A token that is already gone exits 3 (not_found). With --if-exists it exits 0
and prints the id-only object with result "unchanged".

Agent tokens cannot revoke tokens: the server answers 403 (exit 6).`,
		Example:     "  sel tokens revoke 7d0c6a0e-5b1f-4c5e-9d0f-2a1b3c4d5e6f --yes\n  sel tokens revoke ID --dry-run",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6", "mutates": "true", "destructive": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tokenID.MatchString(args[0]) {
				return contract.Errorf(contract.UsageError, "%q is not a token id (a UUID, shown by sel tokens list); to revoke the token this command line uses, run: sel auth logout", args[0])
			}
			if !opts.yes && !opts.dryRun {
				return contract.Errorf(contract.UsageError, "revoking a token needs --yes (or --dry-run to preview)")
			}
			env, err := tokensEnv(cmd, &f)
			if err != nil {
				return err
			}
			return tokensRevoke(env, args[0], opts)
		},
	}
	f.bind(cmd, false)
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "Confirm the revoke (required, with or without a terminal)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Report what would be revoked without revoking it")
	cmd.Flags().BoolVar(&opts.ifExists, "if-exists", false, "Exit 0 with result unchanged when the token is already gone")
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
		printNotices(env, page)
		return nil
	}
	w := tabwriter.NewWriter(env.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPREFIX\tKIND\tSCOPES\tORG\tREPOS\tEXPIRES (UTC)\tAPPROVAL")
	for _, t := range page.Tokens {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Prefix, t.Kind, strings.Join(t.Scopes, ","), orAll(t.Organization), repoList(t.Repos), expiry(t.ExpiresAt), approval(t.RequiresApproval))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	printNotices(env, page)
	return nil
}

func tokensRevoke(env *setup.Env, id string, opts revokeOptions) error {
	revoked, err := env.Client.RevokeToken(id, opts.dryRun)
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && opts.ifExists {
		revoked, err = &api.Revocation{Token: api.RevokedToken{ID: id}, Result: "unchanged", DryRun: opts.dryRun}, nil
	}
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Write(env.Stdout, "token", revoked.Token, revoked.Result, revoked.DryRun)
	}
	fmt.Fprintln(env.Stderr, revokeSentence(revoked))
	return nil
}

func revokeSentence(r *api.Revocation) string {
	label := r.Token.ID
	if r.Token.Name != "" {
		label = fmt.Sprintf("%s (%s)", r.Token.Name, r.Token.Prefix)
	}
	switch {
	case r.Result == "unchanged":
		return fmt.Sprintf("Token %s is already gone.", label)
	case r.DryRun:
		return fmt.Sprintf("Dry run: would revoke token %s.", label)
	}
	return fmt.Sprintf("Revoked token %s.", label)
}

func printNotices(env *setup.Env, page *api.TokensPage) {
	for _, notice := range page.Notices {
		if notice == api.PaginationStoppedEarly {
			notice = fmt.Sprintf("stopped early after %d tokens; more exist on the server", len(page.Tokens))
		}
		fmt.Fprintf(env.Stderr, "Notice: %s\n", notice)
	}
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
	if t.RequiresApproval != nil {
		fmt.Fprintf(w, "Approval:  %s\n", approval(t.RequiresApproval))
	}
	fmt.Fprintf(w, "Source:    %s\nCreated:   %s UTC\n", t.Source, utcMinute(t.InsertedAt))
}

func approval(requires *bool) string {
	switch {
	case requires == nil:
		return ""
	case *requires:
		return "required"
	}
	return "not required"
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
