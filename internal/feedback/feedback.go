package feedback

import (
	"fmt"
	"io"
	"strings"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	MaxBytes = 8192
	maxStdin = 1 << 20
)

type Options struct {
	Org     string
	Version string
	Key     string
	DryRun  bool
}

func Read(arg string, hasArg bool, stdin io.Reader) (string, error) {
	text := arg
	if !hasArg {
		raw, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
		if err != nil {
			return "", contract.Errorf(contract.UsageError, "could not read the feedback from stdin: %v", err)
		}
		text = string(raw)
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", contract.Errorf(contract.UsageError, "no feedback text: pass it as an argument or on stdin")
	case len(text) > MaxBytes:
		return "", contract.Errorf(contract.UsageError, "feedback is limited to %d bytes", MaxBytes)
	}
	return text, nil
}

func Send(env *setup.Env, text string, opts Options) error {
	record := api.FeedbackRecord{Text: text, Source: "cli", ClientVersion: opts.Version}
	sent := &api.Feedback{Feedback: record, Result: "created", DryRun: true}
	if !opts.DryRun {
		org, err := env.ResolveOrg(opts.Org)
		if err != nil {
			return err
		}
		if sent, err = env.Client.SendFeedback(org, opts.Key, record); err != nil {
			return err
		}
	}
	switch {
	case env.JSON:
		return format.Document(env.Stdout, sent)
	case sent.DryRun:
		_, err := fmt.Fprintf(env.Stdout, "Dry run: would send %d bytes of feedback; nothing was sent.\n", len(text))
		return err
	}
	_, err := fmt.Fprintln(env.Stdout, "Thanks, your feedback was sent to the Selective team.")
	return err
}
