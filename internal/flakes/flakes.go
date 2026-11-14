package flakes

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	pageSize               = 200
	maxFlakes              = 20000
	maxRequests            = 2000
	paginationStoppedEarly = "pagination_stopped_early"
	unknown                = "—"
)

var noticeText = map[string]string{
	"approximate_ranking": "the ranking is approximate; order can shift between pages",
	"metrics_unavailable": "some metrics weren't computed in time and show as " + unknown + "; run again to fill them",
}

type ListOptions struct {
	api.FlakesQuery
	Hostname string
	Org      string
}

type flake struct {
	ID           string
	Status       string
	Test         struct{ Name string }
	Suite        struct{ Name string }
	LastSeenAt   string   `json:"last_seen_at"`
	FlakeRate    *float64 `json:"flake_rate"`
	RetryMinutes *float64 `json:"retry_minutes"`
	ImpactScore  *float64 `json:"impact_score"`
}

func List(env *setup.Env, opts ListOptions) error {
	page, err := fetchList(env.Client, opts)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, page)
	}
	return renderList(env, opts, page)
}

func fetchList(client *api.Client, opts ListOptions) (*api.FlakesPage, error) {
	query := opts.FlakesQuery
	single := query.Limit > 0
	if !single {
		query.Limit = pageSize
	}
	merged := &api.FlakesPage{Flakes: []json.RawMessage{}, Notices: []string{}}
	for requests := 0; requests < maxRequests; requests++ {
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
		if len(merged.Flakes) >= maxFlakes {
			break
		}
		query.Cursor = *page.NextCursor
	}
	if !single && merged.NextCursor != nil {
		merged.Notices = append(merged.Notices, paginationStoppedEarly)
	}
	return merged, nil
}

func renderList(env *setup.Env, opts ListOptions, page *api.FlakesPage) error {
	if len(page.Flakes) == 0 {
		fmt.Fprintln(env.Stderr, "No flakes match.")
	} else if err := renderTable(env, page.Flakes); err != nil {
		return err
	}
	for _, notice := range page.Notices {
		text, known := noticeText[notice]
		if !known {
			text = notice
		}
		fmt.Fprintf(env.Stderr, "Notice: %s\n", text)
	}
	if page.NextCursor != nil {
		fmt.Fprintf(env.Stderr, "More results: %s\n", continueCommand(opts, *page.NextCursor))
	}
	return nil
}

func renderTable(env *setup.Env, rows []json.RawMessage) error {
	w := tabwriter.NewWriter(env.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tSUITE\tTEST\tRATE\tRETRY\tIMPACT\tLAST SEEN (UTC)")
	for _, raw := range rows {
		var f flake
		if err := json.Unmarshal(raw, &f); err != nil {
			return unexpected(err)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", f.ID, f.Status, f.Suite.Name, f.Test.Name,
			measure(f.FlakeRate, 100, "%.1f%%"), measure(f.RetryMinutes, 1, "%.1fm"), measure(f.ImpactScore, 1, "%.1f"), utc(f.LastSeenAt))
	}
	return w.Flush()
}

func continueCommand(opts ListOptions, cursor string) string {
	parts := []string{"sel flakes list"}
	for _, flag := range []struct{ name, value string }{
		{"--hostname", opts.Hostname},
		{"--org", opts.Org},
		{"--suite", opts.Suite},
		{"--status", opts.Status},
		{"--sort", opts.Sort},
		{"--age", opts.Age},
		{"--search", opts.Search},
	} {
		if flag.value != "" {
			parts = append(parts, flag.name, shellQuote(flag.value))
		}
	}
	if opts.Limit > 0 {
		parts = append(parts, "--limit", strconv.Itoa(opts.Limit))
	}
	return strings.Join(append(parts, "--cursor", shellQuote(cursor)), " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func measure(v *float64, scale float64, layout string) string {
	if v == nil {
		return unknown
	}
	return fmt.Sprintf(layout, *v*scale)
}

func utc(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.UTC().Format("2006-01-02 15:04")
}

func unexpected(err error) error {
	return contract.Errorf(contract.ServerError, "unexpected flake in the response: %v", err)
}
