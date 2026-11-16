package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
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
	var f setupFlags
	var sets []string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "settings SUITE",
		Short: "Show or change a suite's settings",
		Long: `Show a suite's settings (by name or ID), or change them with --set. The JSON
is {"v":1,"suite":{...},"settings":{...}}, the same document as sel suites show;
a change adds "result" (updated or unchanged) and "dry_run".

--set key=value is repeatable and sends only the fields you name. Keys:
  name, type
  retries.auto_retry_enabled, retries.max_retries
  runs.stale_run_timeout_minutes
  detection.enabled, detection.window_days, detection.stability_window_days
  selection.mode, selection.on_default_branch,
  selection.map_record_interval_hours, selection.ignore_patterns

Booleans are true or false, counts are whole numbers, selection.mode is one of
off, shadow or live, and selection.ignore_patterns is a comma-separated list
(empty clears it). An unknown key or a value of the wrong type exits 5 before
anything is sent; ranges are checked by the server (validation_failed, exit 5,
with the offending fields). --dry-run needs --set, reports what would change
and changes nothing. A change needs a token with the write scope, as an owner
or admin of the organization. Without --set, the read scope is enough.`,
		Example:     "  sel suites settings web-rspec\n  sel suites settings web-rspec --set selection.mode=shadow --set retries.max_retries=3\n  sel suites settings web-rspec --set selection.ignore_patterns=vendor,docs --dry-run --json",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6", "mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := settingsBody(sets)
			if err != nil {
				return err
			}
			if dryRun && len(sets) == 0 {
				return contract.Errorf(contract.UsageError, "--dry-run needs at least one --set")
			}
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			if len(sets) == 0 {
				return suitesShow(env, org, args[0])
			}
			return suitesChangeSettings(env, org, args[0], body, dryRun)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().StringArrayVar(&sets, "set", nil, "Change a setting, as key=value (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change without changing it")
	return cmd
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

var settingParsers = map[string]func(string) (any, error){
	"name":                                parseText,
	"type":                                parseText,
	"retries.auto_retry_enabled":          parseFlag,
	"retries.max_retries":                 parseCount,
	"runs.stale_run_timeout_minutes":      parseCount,
	"detection.enabled":                   parseFlag,
	"detection.window_days":               parseCount,
	"detection.stability_window_days":     parseCount,
	"selection.mode":                      parseMode,
	"selection.on_default_branch":         parseFlag,
	"selection.map_record_interval_hours": parseCount,
	"selection.ignore_patterns":           parseList,
}

func parseText(raw string) (any, error) { return raw, nil }

func parseFlag(raw string) (any, error) {
	if raw != "true" && raw != "false" {
		return nil, contract.Errorf(contract.UsageError, "%q is not true or false", raw)
	}
	return raw == "true", nil
}

func parseCount(raw string) (any, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil, contract.Errorf(contract.UsageError, "%q is not a whole number", raw)
	}
	return n, nil
}

func parseMode(raw string) (any, error) {
	if !slices.Contains(setup.SelectionModes, raw) {
		return nil, contract.Errorf(contract.UsageError, "%q is not one of: %s", raw, strings.Join(setup.SelectionModes, ", "))
	}
	return raw, nil
}

func parseList(raw string) (any, error) {
	if raw == "" {
		return []string{}, nil
	}
	items := strings.Split(raw, ",")
	for i, item := range items {
		items[i] = strings.TrimSpace(item)
		if items[i] == "" {
			return nil, contract.Errorf(contract.UsageError, "%q has an empty element; separate patterns with single commas", raw)
		}
	}
	return items, nil
}

func settingsBody(sets []string) (map[string]any, error) {
	body := map[string]any{}
	for _, set := range sets {
		key, raw, _ := strings.Cut(set, "=")
		parse, known := settingParsers[key]
		if !known || !strings.Contains(set, "=") {
			return nil, contract.Errorf(contract.UsageError, "--set %q: want key=value, with key one of: %s", set, strings.Join(slices.Sorted(maps.Keys(settingParsers)), ", "))
		}
		value, err := parse(raw)
		if err != nil {
			return nil, fmt.Errorf("--set %s: %w", key, err)
		}
		section, field, nested := strings.Cut(key, ".")
		if !nested {
			body[key] = value
			continue
		}
		fields, _ := body[section].(map[string]any)
		if fields == nil {
			fields = map[string]any{}
			body[section] = fields
		}
		fields[field] = value
	}
	return body, nil
}

func suitesChangeSettings(env *setup.Env, org, want string, body map[string]any, dryRun bool) error {
	id, err := suiteIDForArchive(env.Client, org, want, true)
	if err != nil {
		return err
	}
	change, err := env.Client.UpdateSuiteSettings(org, id, body, dryRun)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, change)
	}
	fmt.Fprintln(env.Stdout, settingsSentence(want, change))
	return nil
}

func settingsSentence(name string, change *api.SuiteSettingsChange) string {
	switch {
	case change.Result == "unchanged":
		return fmt.Sprintf("Settings of suite %q already match.", name)
	case change.DryRun:
		return fmt.Sprintf("Dry run: would change settings of suite %q.", name)
	}
	return fmt.Sprintf("Changed settings of suite %q.", name)
}
