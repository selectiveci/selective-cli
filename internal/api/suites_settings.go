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
	if !isJSONObject(detail.Suite) || !isJSONObject(detail.Settings) {
		return nil, &Error{Code: contract.ServerError, Message: "unexpected response from " + path + ": suite and settings must both be objects"}
	}
	return &detail, nil
}

func isJSONObject(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '{'
}
