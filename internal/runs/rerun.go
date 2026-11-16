package runs

import (
	"crypto/rand"
	"fmt"

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
	return format.Document(env.Stdout, rerun)
}
