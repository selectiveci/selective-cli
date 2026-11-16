package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/wait"
)

type Outcome struct {
	Key      string
	Resource any
	Result   string
	DryRun   bool
}

type Gate struct {
	Client  *api.Client
	Out     io.Writer
	Wait    time.Duration
	Started time.Time
	Clock   wait.Clock
}

func (g *Gate) Do(ctx context.Context, send func(context.Context) (*Outcome, error), sentence func(*Outcome) string) error {
	outcome, err := send(ctx)
	var pending *api.PendingApproval
	if !errors.As(err, &pending) {
		if err != nil {
			return err
		}
		if err := format.Write(g.Out, outcome.Key, outcome.Resource, outcome.Result, outcome.DryRun); err != nil {
			return err
		}
		if g.Wait > 0 && !outcome.DryRun {
			return format.EventLine(g.Out, "done", doneFields(outcome))
		}
		return nil
	}
	id, _ := identify(pending)
	if g.Wait <= 0 || pending.DryRun || id == "" {
		return err
	}
	if err := format.Document(g.Out, struct {
		Result   string          `json:"result"`
		DryRun   bool            `json:"dry_run"`
		Approval json.RawMessage `json:"approval"`
	}{"pending_approval", false, pending.Approval}); err != nil {
		return err
	}
	if _, err := Await(ctx, g.Client, id, wait.Options{Limit: g.Wait, Origin: g.Started, Out: g.Out, Clock: g.Clock}); err != nil {
		return err
	}
	outcome, err = send(ctx)
	if errors.As(err, &pending) {
		_, url := identify(pending)
		again := contract.Errorf(contract.PendingApproval, "the re-sent request needs a new approval at %s", url)
		again.Fields = map[string]any{"approval": pending.Approval}
		return again
	}
	if err != nil {
		return err
	}
	return format.EventLine(g.Out, "done", doneFields(outcome))
}

func doneFields(outcome *Outcome) map[string]any {
	return map[string]any{"result": outcome.Result, outcome.Key: outcome.Resource}
}

func identify(p *api.PendingApproval) (id, url string) {
	var approval struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal(p.Approval, &approval)
	return approval.ID, approval.URL
}
