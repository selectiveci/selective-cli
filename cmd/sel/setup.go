package main

import (
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

type setupFlags struct {
	hostname string
	org      string
	json     bool
}

func (f *setupFlags) bind(cmd *cobra.Command, withOrg bool) {
	cmd.Flags().StringVar(&f.hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().BoolVar(&f.json, "json", false, "JSON output (the default when piped)")
	if withOrg {
		cmd.Flags().StringVar(&f.org, "org", "", "Organization slug (default: SELECTIVE_ORG, or your only one)")
	}
}

// env connects and, when asked, resolves the organization.
func (f *setupFlags) env(withOrg bool) (*setup.Env, string, error) {
	env, err := setup.Connect(f.hostname, f.json)
	if err != nil || !withOrg {
		return env, "", err
	}
	org, err := env.ResolveOrg(f.org)
	return env, org, err
}

func orgsCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:         "orgs",
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6,9"},
		Short:       "List your organizations",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _, err := f.env(false)
			if err != nil {
				return err
			}
			return env.Orgs()
		},
	}
	f.bind(cmd, false)
	return cmd
}

func reposCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:         "repos",
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6,9"},
		Short:       "List the repositories Selective's GitHub App can see",
		Long: `List the repositories a suite can be created for: the ones the
organization's Selective GitHub App installation can see. The output ends
with the URL where a signed-in user can add more.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			return env.Repos(org)
		},
	}
	f.bind(cmd, true)
	return cmd
}
