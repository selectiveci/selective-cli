package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

type TrendsQuery struct {
	Range   string
	Suite   string
	Section string
	Limit   int
}

func (c *Client) Trends(org string, q TrendsQuery) (map[string]json.RawMessage, error) {
	params := url.Values{}
	if q.Range != "" {
		params.Set("range", q.Range)
	}
	if q.Suite != "" {
		params.Set("suite", q.Suite)
	}
	if q.Section != "" {
		params.Set("section", q.Section)
	}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	path := "/api/v1/orgs/" + url.PathEscape(org) + "/trends"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out map[string]json.RawMessage
	if err := c.do("GET", path, nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return out, nil
}
