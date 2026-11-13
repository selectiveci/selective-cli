package localrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
)

const (
	OutcomeFull    = "full"
	defaultTimeout = 30 * time.Minute
)

type Case struct {
	ID       string
	FilePath string
}

type Manifest struct {
	Cases []Case
	Tree  json.RawMessage
}

type Selection struct {
	Outcome      string
	Cause        string
	Source       string
	Selected     []string
	ChangedFiles int
}

type Selector func(context.Context, Manifest) (Selection, error)

type Options struct {
	Selector Selector
	Timeout  time.Duration
}

type Summary struct {
	Passed   int
	Failed   int
	Pending  int
	Selected int
	Skipped  int
	MS       int64
}

type inbound struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	err  error
}

func Run(ctx context.Context, in io.Reader, out io.Writer, events io.Writer, opts Options) (Summary, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	r := &run{opts: opts, out: json.NewEncoder(out), events: events, start: time.Now()}
	return r.loop(ctx, readFrames(in, ctx.Done()))
}

type run struct {
	opts        Options
	out         *json.Encoder
	events      io.Writer
	start       time.Time
	manifested  bool
	closed      bool
	outstanding map[string]bool
	completed   int
	total       int
	sum         Summary
}

func (r *run) loop(ctx context.Context, frames <-chan inbound) (Summary, error) {
	for _, command := range []map[string]any{
		{"command": "connected"},
		{"command": "configure_test_map", "tree": true, "record": false},
		{"command": "test_manifest"},
	} {
		if err := r.send(command); err != nil {
			return r.sum, err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return r.sum, r.abort(ctx.Err())
		case frame := <-frames:
			if frame.err != nil {
				return r.sum, r.abort(runnerLost(frame.err))
			}
			done, err := r.handle(ctx, frame)
			if err != nil {
				return r.sum, r.abort(err)
			}
			if done {
				return r.sum, nil
			}
		}
	}
}

func (r *run) handle(ctx context.Context, frame inbound) (bool, error) {
	switch frame.Type {
	case "test_manifest":
		if r.manifested {
			return false, nil
		}
		r.manifested = true
		return false, r.onManifest(ctx, frame.Data)
	case "test_case_result":
		return false, r.onResult(frame.Data)
	case "report_at_finish":
		if !r.closed || len(r.outstanding) > 0 {
			return false, contract.Errorf(contract.RunnerError, "the test runner reported its finish with %d selected tests still outstanding", len(r.outstanding))
		}
		r.sum.MS = time.Since(r.start).Milliseconds()
		r.event("summary", r.summaryFields())
		return true, nil
	}
	return false, nil
}

func (r *run) onManifest(ctx context.Context, data json.RawMessage) error {
	var raw struct {
		TestCases []struct {
			ID       string `json:"id"`
			FilePath string `json:"file_path"`
		} `json:"test_cases"`
		Tree json.RawMessage `json:"test_map_tree"`
	}
	if err := decode(data, &raw); err != nil {
		return err
	}
	manifest := Manifest{Tree: raw.Tree}
	for _, c := range raw.TestCases {
		manifest.Cases = append(manifest.Cases, Case{ID: c.ID, FilePath: c.FilePath})
	}
	selection, err := r.opts.Selector(ctx, manifest)
	if err != nil {
		return err
	}
	ids := selection.Selected
	if selection.Outcome == OutcomeFull {
		ids = nil
		for _, c := range manifest.Cases {
			ids = append(ids, c.ID)
		}
	}
	r.outstanding = make(map[string]bool, len(ids))
	var unique []string
	for _, id := range ids {
		if !r.outstanding[id] {
			r.outstanding[id] = true
			unique = append(unique, id)
		}
	}
	ids = unique
	r.total = len(ids)
	r.sum.Selected = len(ids)
	r.sum.Skipped = max(len(manifest.Cases)-len(ids), 0)
	r.event("selection", selectionFields(selection, r.sum, len(manifest.Cases)))
	if len(ids) == 0 {
		return r.close()
	}
	return r.send(map[string]any{"command": "run_test_cases", "test_case_ids": ids})
}

func (r *run) onResult(data json.RawMessage) error {
	var res struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := decode(data, &res); err != nil {
		return err
	}
	if !r.outstanding[res.ID] {
		return nil
	}
	switch res.Status {
	case "failed":
		r.sum.Failed++
	case "pending":
		r.sum.Pending++
	case "passed":
		r.sum.Passed++
	default:
		return contract.Errorf(contract.RunnerError, "the test runner sent status %q for %s", res.Status, res.ID)
	}
	delete(r.outstanding, res.ID)
	r.completed++
	if len(r.outstanding) == 0 {
		return r.close()
	}
	return nil
}

func (r *run) close() error {
	r.closed = true
	return r.send(map[string]any{"command": "close", "exit_status": nil})
}

func (r *run) send(command map[string]any) error {
	if err := r.out.Encode(command); err != nil {
		return runnerLost(err)
	}
	return nil
}

func (r *run) abort(cause error) error {
	if !r.closed {
		r.closed = true
		_ = r.out.Encode(map[string]any{"command": "close", "exit_status": nil})
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return contract.Errorf(contract.Timeout, "the test run did not finish: %d of %d selected tests had reported", r.completed, r.total)
	}
	return cause
}

func (r *run) event(name string, fields map[string]any) {
	_ = format.EventLine(r.events, name, fields)
}

func decode(data json.RawMessage, into any) error {
	if err := json.Unmarshal(data, into); err != nil {
		return contract.Errorf(contract.RunnerError, "the test runner sent a frame that could not be read: %v", err)
	}
	return nil
}

func runnerLost(cause error) error {
	return contract.Errorf(contract.RunnerError, "the test runner went away before the run finished: %v", cause)
}

func readFrames(in io.Reader, done <-chan struct{}) <-chan inbound {
	frames := make(chan inbound)
	go func() {
		reader := bufio.NewReader(in)
		for {
			line, err := reader.ReadBytes('\n')
			var frame inbound
			switch {
			case err != nil:
				frame.err = err
			case len(line) <= 1:
				continue
			default:
				if uerr := json.Unmarshal(line, &frame); uerr != nil {
					frame = inbound{err: uerr}
				}
			}
			select {
			case frames <- frame:
			case <-done:
				return
			}
			if frame.err != nil {
				return
			}
		}
	}()
	return frames
}
