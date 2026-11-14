package runs

import (
	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

type TrendsOptions struct {
	Org     string
	Range   string
	Suite   string
	Section string
	Limit   int
}

func Trends(env *setup.Env, opts TrendsOptions) error {
	org, err := env.ResolveOrg(opts.Org)
	if err != nil {
		return err
	}
	document, err := env.Client.Trends(org, api.TrendsQuery{Range: opts.Range, Suite: opts.Suite, Section: opts.Section, Limit: opts.Limit})
	if err != nil {
		return err
	}
	return format.Document(env.Stdout, document)
}
