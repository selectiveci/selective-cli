package api

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type SuiteDetail struct {
	Suite    json.RawMessage `json:"suite"`
	Settings json.RawMessage `json:"settings"`
}

func (c *Client) ShowSuite(org, id string) (*SuiteDetail, error) {
	var detail SuiteDetail
	path := "/api/v1/orgs/" + url.PathEscape(org) + "/suites/" + url.PathEscape(id)
	if err := c.do(http.MethodGet, path, nil, http.StatusOK, &detail); err != nil {
		return nil, err
	}
	if err := detail.check(path); err != nil {
		return nil, err
	}
	return &detail, nil
}

func (d SuiteDetail) check(path string) error {
	if !isJSONObject(d.Suite) || !isJSONObject(d.Settings) {
		return &Error{Code: contract.ServerError, Message: "unexpected response from " + path + ": suite and settings must both be objects"}
	}
	return nil
}

type SuiteSettingsChange struct {
	SuiteDetail
	Result string `json:"result"`
	DryRun bool   `json:"dry_run"`
}

func (c *Client) UpdateSuiteSettings(org, id string, body map[string]any, dryRun bool) (*SuiteSettingsChange, error) {
	var change SuiteSettingsChange
	path := withQuery("/api/v1/orgs/"+url.PathEscape(org)+"/suites/"+url.PathEscape(id)+"/settings", dryRunQuery(dryRun))
	if err := c.do(http.MethodPut, path, body, http.StatusOK, &change); err != nil {
		return nil, err
	}
	if err := change.check(path); err != nil {
		return nil, err
	}
	return &change, nil
}

func isJSONObject(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '{'
}
