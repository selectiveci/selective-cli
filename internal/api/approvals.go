package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

const (
	approvalsPageSize   = 200
	maxApprovals        = 20000
	maxApprovalRequests = 2000
)

type ApprovalSubject struct {
	Type  string  `json:"type"`
	ID    string  `json:"id"`
	Label *string `json:"label"`
}

type ApprovalRequester struct {
	TokenID   *string `json:"token_id"`
	TokenName *string `json:"token_name"`
	UserID    *string `json:"user_id"`
}

type Approval struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Status          string            `json:"status"`
	Action          string            `json:"action"`
	Subject         ApprovalSubject   `json:"subject"`
	Params          json.RawMessage   `json:"params"`
	RequestedBy     ApprovalRequester `json:"requested_by"`
	ExpiresAt       *string           `json:"expires_at"`
	DecidedAt       *string           `json:"decided_at"`
	DecidedByUserID *string           `json:"decided_by_user_id"`
	DecisionNote    *string           `json:"decision_note"`
	ConsumedAt      *string           `json:"consumed_at"`
	InsertedAt      string            `json:"inserted_at"`
}

func (c *Client) GetApproval(id string) (*Approval, error) {
	var out struct {
		Approval Approval `json:"approval"`
	}
	if err := c.do("GET", "/api/v1/approvals/"+url.PathEscape(id), nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Approval, nil
}

type ApprovalsPage struct {
	Approvals  []Approval `json:"approvals"`
	NextCursor *string    `json:"next_cursor"`
	Notices    []string   `json:"notices,omitempty"`
}

func (c *Client) Approvals(org, status string) (*ApprovalsPage, error) {
	merged := &ApprovalsPage{Approvals: []Approval{}}
	params := url.Values{"limit": {strconv.Itoa(approvalsPageSize)}}
	if status != "" {
		params.Set("status", status)
	}
	path := "/api/v1/orgs/" + url.PathEscape(org) + "/approvals"
	for requests := 0; requests < maxApprovalRequests; requests++ {
		var page ApprovalsPage
		if err := c.do("GET", withQuery(path, params), nil, http.StatusOK, &page); err != nil {
			return nil, err
		}
		merged.Approvals = append(merged.Approvals, page.Approvals...)
		merged.Notices = appendMissing(merged.Notices, page.Notices...)
		merged.NextCursor = page.NextCursor
		if page.NextCursor == nil || len(merged.Approvals) >= maxApprovals {
			break
		}
		params.Set("cursor", *page.NextCursor)
	}
	if merged.NextCursor != nil {
		merged.Notices = appendMissing(merged.Notices, PaginationStoppedEarly)
	}
	return merged, nil
}
