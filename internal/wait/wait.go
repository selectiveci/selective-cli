package wait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
)

const (
	MinInterval = 2 * time.Second
	MaxInterval = 15 * time.Second

	FinalCheckGrace = time.Second
)

type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type Options struct {
	For    string
	Limit  time.Duration
	Origin time.Time
	Out    io.Writer
	Pretty bool
	Clock  Clock
}

type Check func(ctx context.Context) (done bool, retryAfter time.Duration, err error)

type ExpiredError struct {
	For   string
	Limit time.Duration
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("gave up waiting for %s after %s", e.For, e.Limit)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func RetryAfter(err error) time.Duration {
	var fielded contract.Fielded
	if !errors.As(err, &fielded) {
		return 0
	}
	var seconds float64
	switch v := fielded.ErrorFields()["retry_after_s"].(type) {
	case int:
		seconds = float64(v)
	case float64:
		seconds = v
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func Poll(ctx context.Context, opts Options, check Check) error {
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	start := clock.Now()
	origin := opts.Origin
	if origin.IsZero() {
		origin = start
	}
	delay := MinInterval
	for {
		done, retryAfter, err := runCheck(ctx, clock, start, opts, check)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		now := clock.Now()
		remaining := opts.Limit - now.Sub(start)
		step := max(delay, retryAfter)
		if remaining <= 0 || retryAfter > remaining {
			return &ExpiredError{For: opts.For, Limit: opts.Limit}
		}
		if err := opts.announce(now.Sub(origin)); err != nil {
			return err
		}
		if err := clock.Sleep(ctx, min(step, remaining)); err != nil {
			return err
		}
		delay = min(delay*2, MaxInterval)
	}
}

func (o Options) announce(elapsed time.Duration) error {
	if o.Out == nil {
		return nil
	}
	seconds := int(elapsed.Seconds())
	if o.Pretty {
		_, err := fmt.Fprintf(o.Out, "waiting for %s (%ds)\n", o.For, seconds)
		return err
	}
	return format.EventLine(o.Out, "waiting", map[string]any{"elapsed_s": seconds, "for": o.For})
}

func runCheck(ctx context.Context, clock Clock, start time.Time, opts Options, check Check) (bool, time.Duration, error) {
	if opts.Limit <= 0 {
		return check(ctx)
	}
	remaining := opts.Limit - clock.Now().Sub(start)
	if remaining <= 0 {
		remaining = FinalCheckGrace
	}
	budget, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	done, retryAfter, err := check(budget)
	if errors.Is(err, context.DeadlineExceeded) && budget.Err() != nil && ctx.Err() == nil {
		return false, 0, &ExpiredError{For: opts.For, Limit: opts.Limit}
	}
	return done, retryAfter, err
}
