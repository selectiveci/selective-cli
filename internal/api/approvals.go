package api

import (
	"encoding/json"
	"net/http"
	"net/url"
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
