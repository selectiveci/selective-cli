package api

import (
	"encoding/json"
	"net/http"
	"net/url"
)

type SuiteChange struct {
	Suite  json.RawMessage
	Name   string
	Result string
	DryRun bool
}

func (c *Client) SuitesIn(org, state string) ([]Suite, error) {
	var out struct {
		Suites []Suite `json:"suites"`
	}
	path := withQuery("/api/v1/orgs/"+url.PathEscape(org)+"/suites", url.Values{"state": {state}})
	err := c.do("GET", path, nil, http.StatusOK, &out)
	return out.Suites, err
}

func (c *Client) ArchiveSuite(org, id string, dryRun bool) (*SuiteChange, error) {
	params := url.Values{}
	if dryRun {
		params.Set("dry_run", "true")
	}
	var wire struct {
		Suite  json.RawMessage `json:"suite"`
		Result string          `json:"result"`
		DryRun bool            `json:"dry_run"`
	}
	path := withQuery("/api/v1/orgs/"+url.PathEscape(org)+"/suites/"+url.PathEscape(id)+"/archive", params)
	if err := c.do(http.MethodPut, path, nil, http.StatusOK, &wire); err != nil {
		return nil, err
	}
	var named struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(wire.Suite, &named)
	return &SuiteChange{Suite: wire.Suite, Name: named.Name, Result: wire.Result, DryRun: wire.DryRun}, nil
}
