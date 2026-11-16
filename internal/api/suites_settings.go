package api

import (
	"encoding/json"
	"net/http"
	"net/url"
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
	return &detail, nil
}
