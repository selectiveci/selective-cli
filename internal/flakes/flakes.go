package flakes

import (
	"encoding/json"
	"slices"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	pageSize = 200
	maxPages = 100
)

type ListOptions struct {
	api.FlakesQuery
	Hostname string
	Org      string
}

func List(env *setup.Env, opts ListOptions) error {
	page, err := fetchList(env.Client, opts)
	if err != nil {
		return err
	}
	return format.Document(env.Stdout, page)
}

func fetchList(client *api.Client, opts ListOptions) (*api.FlakesPage, error) {
	query := opts.FlakesQuery
	single := query.Limit > 0
	if !single {
		query.Limit = pageSize
	}
	merged := &api.FlakesPage{Flakes: []json.RawMessage{}, Notices: []string{}}
	for pages := 0; pages < maxPages; pages++ {
		page, err := client.Flakes(opts.Org, query)
		if err != nil {
			return nil, err
		}
		merged.Flakes = append(merged.Flakes, page.Flakes...)
		for _, notice := range page.Notices {
			if !slices.Contains(merged.Notices, notice) {
				merged.Notices = append(merged.Notices, notice)
			}
		}
		merged.NextCursor = page.NextCursor
		if single || page.NextCursor == nil {
			break
		}
		query.Cursor = *page.NextCursor
	}
	return merged, nil
}
