package api

import (
	"net/http"
	"net/url"
)

type FeedbackRecord struct {
	ID            string `json:"id,omitempty"`
	Text          string `json:"text,omitempty"`
	Source        string `json:"source,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
}

type Feedback struct {
	Feedback FeedbackRecord `json:"feedback"`
	Result   string         `json:"result"`
	DryRun   bool           `json:"dry_run"`
}

func (c *Client) SendFeedback(org, key string, body FeedbackRecord) (*Feedback, error) {
	var out Feedback
	header := http.Header{"Idempotency-Key": {key}}
	if err := c.doWith(header, "POST", "/api/v1/orgs/"+url.PathEscape(org)+"/feedback", body, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
