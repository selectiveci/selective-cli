package runs

import (
	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	pageSize = 200
	maxPages = 100
)

type FailuresOptions struct {
	Build    string
	Hostname string
	Org      string
	Suite    string
	Outcome  string
	Limit    int
	Cursor   string
}

func Failures(env *setup.Env, opts FailuresOptions) error {
	page, err := fetchFailures(env.Client, opts)
	if err != nil {
		return err
	}
	return format.Document(env.Stdout, page)
}

func fetchFailures(client *api.Client, opts FailuresOptions) (*api.FailuresPage, error) {
	query := api.FailuresQuery{Org: opts.Org, Outcome: opts.Outcome, Limit: opts.Limit, Cursor: opts.Cursor}
	single := opts.Limit > 0
	if !single {
		query.Limit = pageSize
	}
	var merged *api.FailuresPage
	for pages := 0; pages < maxPages; pages++ {
		page, err := client.Failures(opts.Build, query)
		if err != nil {
			return nil, err
		}
		page.Failures = filterSuite(page.Failures, opts.Suite)
		if merged != nil {
			page.Failures = append(merged.Failures, page.Failures...)
		}
		merged = page
		if single || page.NextCursor == nil {
			break
		}
		query.Cursor = *page.NextCursor
	}
	if merged.Failures == nil {
		merged.Failures = []api.Failure{}
	}
	return merged, nil
}

func filterSuite(failures []api.Failure, suite string) []api.Failure {
	if suite == "" {
		return failures
	}
	kept := []api.Failure{}
	for _, f := range failures {
		if f.Suite.Name == suite || f.Suite.ID == suite {
			kept = append(kept, f)
		}
	}
	return kept
}
