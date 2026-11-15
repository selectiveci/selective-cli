package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const pendingApprovalResult = "pending_approval"

type PendingApproval struct {
	DryRun   bool
	Approval json.RawMessage
}

func (p *PendingApproval) Error() string {
	return "the server needs a person's approval before it acts"
}

func (p *PendingApproval) ErrorCode() contract.Code { return contract.PendingApproval }
func (p *PendingApproval) ExitCode() int            { return contract.ExitPending }

func pendingResponse(status int, body []byte, path string) error {
	var probe struct {
		Result   string          `json:"result"`
		DryRun   bool            `json:"dry_run"`
		Approval json.RawMessage `json:"approval"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Result == pendingApprovalResult {
		if string(probe.Approval) == "null" {
			probe.Approval = nil
		}
		return &PendingApproval{DryRun: probe.DryRun, Approval: probe.Approval}
	}
	if status == http.StatusAccepted {
		return &Error{Status: status, Code: contract.ServerError, Message: fmt.Sprintf("unexpected 202 from %s: no pending approval in the body", path)}
	}
	return nil
}
