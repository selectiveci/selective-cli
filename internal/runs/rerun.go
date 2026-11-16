package runs

import (
	"crypto/rand"
	"encoding/json"
	"fmt"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

type RerunOptions struct {
	Build  string
	Org    string
	DryRun bool
	Key    string
}

func NewIdempotencyKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not make an idempotency key: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func Rerun(env *setup.Env, opts RerunOptions) error {
	rerun, err := env.Client.RerunFailed(opts.Build, opts.Org, opts.Key, opts.DryRun)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, rerun)
	}
	fmt.Fprintln(env.Stderr, rerunSentence(rerun))
	return nil
}

func rerunSentence(r *api.Rerun) string {
	var body struct {
		Enqueued int `json:"enqueued"`
	}
	_ = json.Unmarshal(r.Rerun, &body)
	switch {
	case r.Result == "unchanged":
		return fmt.Sprintf("Nothing to queue for build %s: its failed tests are already pending.", r.BuildID)
	case r.DryRun:
		return fmt.Sprintf("Dry run: would queue %d failed test(s) of build %s for rerun.", body.Enqueued, r.BuildID)
	}
	return fmt.Sprintf("Queued %d failed test(s) of build %s for rerun.", body.Enqueued, r.BuildID)
}
