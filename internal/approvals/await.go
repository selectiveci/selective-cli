package approvals

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/wait"
)

func Await(ctx context.Context, client *api.Client, id string, opts wait.Options) (*api.Approval, error) {
	opts.For = "approval"
	var latest *api.Approval
	var lastTransient error
	polling := client.WithoutRateLimitRetry()
	err := wait.Poll(ctx, opts, func(checkCtx context.Context) (bool, time.Duration, error) {
		approval, err := polling.WithContext(checkCtx).GetApproval(id)
		lastTransient = nil
		var apiErr *api.Error
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusTooManyRequests || apiErr.Code == contract.RateLimited) {
			lastTransient = err
			return false, wait.RetryAfter(err), nil
		}
		if api.Transient(err) && ctx.Err() == nil {
			lastTransient = err
			return false, 0, nil
		}
		if err != nil {
			return false, 0, err
		}
		latest = approval
		return approval.Status != "pending", 0, nil
	})
	var expired *wait.ExpiredError
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, context.Cause(ctx)
	case errors.As(err, &expired) && lastTransient != nil:
		return nil, lastTransient
	case expired != nil:
		return nil, carrying(contract.PendingApproval, latest, "still pending after %s; a person must decide it at %s", expired.Limit, latest.URL)
	case err != nil:
		return nil, err
	}
	switch latest.Status {
	case "approved":
		return latest, nil
	case "denied":
		if latest.DecisionNote != nil {
			return nil, carrying(contract.ApprovalDenied, latest, "approval denied: %s", *latest.DecisionNote)
		}
		return nil, carrying(contract.ApprovalDenied, latest, "approval denied")
	case "expired":
		if latest.DecidedAt != nil {
			return nil, carrying(contract.ApprovalExpired, latest, "approval was approved at %s but not used before it expired; request the action again", *latest.DecidedAt)
		}
		return nil, carrying(contract.ApprovalExpired, latest, "approval expired before anyone decided it; request the action again")
	}
	return nil, contract.Errorf(contract.InternalError, "approval has an unknown status %q", latest.Status)
}

func carrying(code contract.Code, approval *api.Approval, format string, args ...any) *contract.Error {
	err := contract.Errorf(code, format, args...)
	err.Fields = map[string]any{"approval": approval}
	return err
}
