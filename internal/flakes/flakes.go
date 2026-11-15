package flakes

import (
	"encoding/json"
	"fmt"
	"io"
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
	ID              string
	Status          string
	Test            struct{ Name, Path string }
	Suite           struct{ Name string }
	FirstSeenAt     string   `json:"first_seen_at"`
	LastSeenAt      string   `json:"last_seen_at"`
	FlakeRate       *float64 `json:"flake_rate"`
	RetryMinutes    *float64 `json:"retry_minutes"`
	ImpactScore     *float64 `json:"impact_score"`
	OccurrenceCount int      `json:"occurrence_count"`
	OrgSlug         string   `json:"org_slug"`
	Occurrences     []struct {
		DetectedAt     string  `json:"detected_at"`
		Branch         string  `json:"branch"`
		CommitSHA      string  `json:"commit_sha"`
		BuildID        string  `json:"build_id"`
		Actor          *string `json:"actor"`
		FailureMessage *string `json:"failure_message"`
	}
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

func Show(env *setup.Env, id string, history *int) error {
	raw, err := env.Client.Flake(id, history)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, struct {
			Flake json.RawMessage `json:"flake"`
		}{raw})
	}
	var f flake
	if err := json.Unmarshal(raw, &f); err != nil {
		return unexpected(err)
	}
	renderShow(env.Stdout, f)
	return nil
}

func Prompt(env *setup.Env, id string, text bool) error {
	prompt, err := env.Client.FlakePrompt(id)
	if err != nil {
		return err
	}
	if text || !env.JSON {
		_, err := io.WriteString(env.Stdout, prompt.Text)
		return err
	}
	return format.Document(env.Stdout, struct {
		Prompt *api.FlakePrompt `json:"prompt"`
	}{prompt})
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
		fmt.Fprintf(env.Stderr, "Notice: %s\n", noticeSentence(notice, len(page.Flakes)))
	}
	if page.NextCursor != nil {
		fmt.Fprintf(env.Stderr, "More results: %s\n", continueCommand(opts, *page.NextCursor))
	}
	return nil
}

func noticeSentence(notice string, count int) string {
	if notice == paginationStoppedEarly {
		return fmt.Sprintf("stopped early after %d flakes; run the command below to continue", count)
	}
	if text, known := noticeText[notice]; known {
		return text
	}
	return notice
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

func renderShow(w io.Writer, f flake) {
	fmt.Fprintf(w, "%s\n  %s\n\n", f.Test.Name, f.Test.Path)
	fmt.Fprintf(w, "ID:           %s\nStatus:       %s\nSuite:        %s (%s)\n", f.ID, f.Status, f.Suite.Name, f.OrgSlug)
	fmt.Fprintf(w, "Flake rate:   %s\nRetry cost:   %s\nImpact:       %s\n",
		measure(f.FlakeRate, 100, "%.1f%%"), measure(f.RetryMinutes, 1, "%.1f min"), measure(f.ImpactScore, 1, "%.1f"))
	fmt.Fprintf(w, "Occurrences:  %d (first seen %s, last seen %s UTC)\n", f.OccurrenceCount, utc(f.FirstSeenAt), utc(f.LastSeenAt))
	if len(f.Occurrences) == 0 {
		return
	}
	fmt.Fprintf(w, "\nRecent failures:\n")
	for _, o := range f.Occurrences {
		fmt.Fprintf(w, "  %s  %s  %.7s  build %s  %s\n", utc(o.DetectedAt), o.Branch, o.CommitSHA, o.BuildID, orUnknown(o.Actor))
		if o.FailureMessage != nil {
			line, _, _ := strings.Cut(strings.TrimSpace(*o.FailureMessage), "\n")
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
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

func orUnknown(s *string) string {
	if s == nil {
		return unknown
	}
	return *s
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
