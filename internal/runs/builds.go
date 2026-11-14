package runs

import (
	"encoding/json"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

type BuildsOptions struct {
	Org    string
	Status string
	Flaky  *bool
	Branch string
	Limit  int
	Cursor string
	All    bool
}

func Builds(env *setup.Env, opts BuildsOptions) error {
	org, err := env.ResolveOrg(opts.Org)
	if err != nil {
		return err
	}
	page, err := fetchBuilds(env.Client, org, opts)
	if err != nil {
		return err
	}
	return format.Document(env.Stdout, page)
}

func fetchBuilds(client *api.Client, org string, opts BuildsOptions) (*api.BuildsPage, error) {
	query := api.BuildsQuery{Status: opts.Status, Flaky: opts.Flaky, Branch: opts.Branch, Limit: opts.Limit}
	if opts.All && query.Limit == 0 {
		query.Limit = pageSize
	}
	var last *api.BuildsPage
	builds, next, stoppedEarly, err := walk(opts.Cursor, opts.All, func(cursor string) ([]json.RawMessage, *string, error) {
		query.Cursor = cursor
		page, err := client.Builds(org, query)
		if err != nil {
			return nil, nil, err
		}
		last = page
		return page.Builds, page.NextCursor, nil
	})
	if err != nil {
		return nil, err
	}
	last.Builds = builds
	if last.Builds == nil {
		last.Builds = []json.RawMessage{}
	}
	last.NextCursor = next
	if stoppedEarly {
		last.Notices = append(last.Notices, paginationStoppedEarly)
	}
	return last, nil
}
