package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

var suiteReadAnnotations = map[string]string{"exit_codes": "0,3,4,5,6"}

func suitesShowCmd() *cobra.Command {
	return suitesReadCmd("show", "Show a suite and its settings", `Show a suite (by name or ID), archived or not, with its settings: retries,
runs, detection and selection. The JSON is {"v":1,"suite":{...},"settings":{...}},
the same document as sel suites settings. Needs a token with the read scope.`)
}

func suitesSettingsCmd() *cobra.Command {
	return suitesReadCmd("settings", "Show a suite's settings", `Show a suite's settings (by name or ID): retries, runs, detection and
selection. The JSON is {"v":1,"suite":{...},"settings":{...}}, the same
document as sel suites show. Needs a token with the read scope.`)
}

func suitesReadCmd(verb, short, long string) *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:         verb + " SUITE",
		Short:       short,
		Long:        long,
		Example:     fmt.Sprintf("  sel suites %s web-rspec\n  sel suites %s web-rspec --json", verb, verb),
		Args:        cobra.ExactArgs(1),
		Annotations: suiteReadAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return suitesShow(env, org, args[0])
		},
	}
	f.bind(cmd, true)
	return cmd
}

func suitesShow(env *setup.Env, org, want string) error {
	id, err := suiteIDForArchive(env.Client, org, want, true)
	if err != nil {
		return err
	}
	detail, err := env.Client.ShowSuite(org, id)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, detail)
	}
	return writeSuiteDetail(env, detail.Suite, detail.Settings)
}

func writeSuiteDetail(env *setup.Env, suiteRaw, settingsRaw json.RawMessage) error {
	var suite struct {
		ID         string  `json:"id"`
		Name       string  `json:"name"`
		Type       string  `json:"type"`
		ArchivedAt *string `json:"archived_at"`
	}
	var settings map[string]any
	if err := json.Unmarshal(suiteRaw, &suite); err != nil {
		return err
	}
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		return err
	}
	state := "active"
	if suite.ArchivedAt != nil {
		state = "archived " + *suite.ArchivedAt
	}
	fmt.Fprintf(env.Stdout, "%s (%s, %s) %s\n", suite.Name, suite.Type, state, suite.ID)
	for _, section := range slices.Sorted(maps.Keys(settings)) {
		fields, ok := settings[section].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			fmt.Fprintf(env.Stdout, "  %s.%s: %v\n", section, key, fields[key])
		}
	}
	return nil
}
