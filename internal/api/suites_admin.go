package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

type SuiteChange struct {
	Suite  json.RawMessage
	Name   string
	Result string
	DryRun bool
}

type ListedSuite struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ArchivedAt *time.Time `json:"archived_at"`
}

func (c *Client) SuitesIn(org, state string) ([]ListedSuite, error) {
	var out struct {
		Suites []ListedSuite `json:"suites"`
	}
	path := withQuery("/api/v1/orgs/"+url.PathEscape(org)+"/suites", url.Values{"state": {state}})
	err := c.do("GET", path, nil, http.StatusOK, &out)
	return out.Suites, err
}

func (c *Client) SetSuiteArchived(org, id string, archived, dryRun bool) (*SuiteChange, error) {
	method := http.MethodDelete
	if archived {
		method = http.MethodPut
	}
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
	if err := c.do(method, path, nil, http.StatusOK, &wire); err != nil {
		return nil, err
	}
	var named struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(wire.Suite, &named)
	return &SuiteChange{Suite: wire.Suite, Name: named.Name, Result: wire.Result, DryRun: wire.DryRun}, nil
}
