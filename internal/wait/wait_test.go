package wait

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

var errRunaway = errors.New("polled past any sane bound")

type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(c.sleeps) >= 100 {
		return errRunaway
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func seconds(ns ...int) []time.Duration {
	out := make([]time.Duration, len(ns))
	for i, n := range ns {
		out[i] = time.Duration(n) * time.Second
	}
	return out
}

func doneAfter(polls int, retryAfter map[int]time.Duration) (Check, *int) {
	calls := 0
	return func(context.Context) (bool, time.Duration, error) {
		calls++
		return calls > polls, retryAfter[calls], nil
	}, &calls
}

func TestPollBacksOffFromTwoToFifteenSeconds(t *testing.T) {
	clock := &fakeClock{}
	check, calls := doneAfter(6, nil)

	err := Poll(context.Background(), Options{Limit: time.Hour, Clock: clock}, check)

	if err != nil {
		t.Fatalf("Poll = %v, want nil", err)
	}
	if want := seconds(2, 4, 8, 15, 15, 15); !slices.Equal(clock.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", clock.sleeps, want)
	}
	if *calls != 7 {
		t.Errorf("checks = %d, want 7", *calls)
	}
}

func TestPollReturnsAtOnceWhenTheThingExists(t *testing.T) {
	clock := &fakeClock{}
	var out bytes.Buffer
	check, calls := doneAfter(0, nil)

	err := Poll(context.Background(), Options{Limit: time.Minute, Out: &out, Clock: clock}, check)

	if err != nil || *calls != 1 || len(clock.sleeps) != 0 || out.Len() != 0 {
		t.Errorf("err=%v checks=%d sleeps=%v out=%q, want one check and nothing else", err, *calls, clock.sleeps, out.String())
	}
}

func TestPollHonoursARetryAfterLongerThanTheCap(t *testing.T) {
	clock := &fakeClock{}
	check, _ := doneAfter(3, map[int]time.Duration{2: 40 * time.Second})

	err := Poll(context.Background(), Options{Limit: time.Hour, Clock: clock}, check)

	if err != nil {
		t.Fatalf("Poll = %v, want nil", err)
	}
	if want := seconds(2, 40, 8); !slices.Equal(clock.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", clock.sleeps, want)
	}
}

func TestPollKeepsItsBackoffWhenRetryAfterIsShorter(t *testing.T) {
	clock := &fakeClock{}
	check, _ := doneAfter(2, map[int]time.Duration{1: time.Second})

	if err := Poll(context.Background(), Options{Limit: time.Hour, Clock: clock}, check); err != nil {
		t.Fatalf("Poll = %v, want nil", err)
	}
	if want := seconds(2, 4); !slices.Equal(clock.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", clock.sleeps, want)
	}
}

func TestPollExpiresAtTheLimitWithADistinctError(t *testing.T) {
	clock := &fakeClock{}
	start := clock.now
	check, _ := doneAfter(1000, nil)

	err := Poll(context.Background(), Options{For: "build", Limit: 20 * time.Second, Clock: clock}, check)

	var expired *ExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("Poll = %v, want *ExpiredError", err)
	}
	if expired.For != "build" || expired.Limit != 20*time.Second {
		t.Errorf("expired = %+v, want For build and Limit 20s", expired)
	}
	if want := seconds(2, 4, 8, 6); !slices.Equal(clock.sleeps, want) {
		t.Errorf("sleeps = %v, want %v (the last one cut to the budget)", clock.sleeps, want)
	}
	if elapsed := clock.now.Sub(start); elapsed != 20*time.Second {
		t.Errorf("elapsed = %s, want exactly 20s", elapsed)
	}
}

func TestPollExpiresRatherThanSleepPastTheLimitForARetryAfter(t *testing.T) {
	clock := &fakeClock{}
	check, calls := doneAfter(1000, map[int]time.Duration{2: 60 * time.Second})

	err := Poll(context.Background(), Options{Limit: 30 * time.Second, Clock: clock}, check)

	var expired *ExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("Poll = %v, want *ExpiredError", err)
	}
	if want := seconds(2); !slices.Equal(clock.sleeps, want) || *calls != 2 {
		t.Errorf("sleeps = %v checks = %d, want [2s] and 2 checks", clock.sleeps, *calls)
	}
}

func TestPollPassesACheckErrorStraightThrough(t *testing.T) {
	clock := &fakeClock{}
	boom := errors.New("boom")
	calls := 0

	err := Poll(context.Background(), Options{Limit: time.Hour, Clock: clock}, func(context.Context) (bool, time.Duration, error) {
		calls++
		return false, 0, boom
	})

	if !errors.Is(err, boom) || calls != 1 || len(clock.sleeps) != 0 {
		t.Errorf("err=%v checks=%d sleeps=%v, want boom after one check", err, calls, clock.sleeps)
	}
}

func TestPollStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	err := Poll(ctx, Options{Limit: time.Hour, Clock: &fakeClock{}}, func(context.Context) (bool, time.Duration, error) {
		calls++
		cancel()
		return false, 0, nil
	})

	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err=%v checks=%d, want context.Canceled after one check", err, calls)
	}
}

func TestPollWritesOneWaitingLinePerPoll(t *testing.T) {
	var out bytes.Buffer
	check, _ := doneAfter(1000, nil)

	_ = Poll(context.Background(), Options{For: "build", Limit: 7 * time.Second, Out: &out, Clock: &fakeClock{}}, check)

	want := `{"v":1,"event":"waiting","elapsed_s":0,"for":"build"}` + "\n" +
		`{"v":1,"event":"waiting","elapsed_s":2,"for":"build"}` + "\n" +
		`{"v":1,"event":"waiting","elapsed_s":6,"for":"build"}` + "\n"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestPollWritesPlainTextInPrettyMode(t *testing.T) {
	var out bytes.Buffer
	check, _ := doneAfter(1, nil)

	_ = Poll(context.Background(), Options{For: "build", Limit: time.Hour, Out: &out, Pretty: true, Clock: &fakeClock{}}, check)

	if want := "waiting for build (0s)\n"; out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
}

func TestRetryAfterReadsTheSecondsAnErrorCarries(t *testing.T) {
	carries := &contract.Error{Code: contract.RateLimited, Fields: map[string]any{"retry_after_s": 7}}
	cases := map[string]struct {
		err  error
		want time.Duration
	}{
		"seconds":      {carries, 7 * time.Second},
		"wrapped":      {errors.Join(errors.New("ctx"), carries), 7 * time.Second},
		"no field":     {&contract.Error{Code: contract.RateLimited}, 0},
		"not a number": {&contract.Error{Fields: map[string]any{"retry_after_s": "soon"}}, 0},
		"negative":     {&contract.Error{Fields: map[string]any{"retry_after_s": -3}}, 0},
		"not fielded":  {errors.New("plain"), 0},
		"nil":          {nil, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := RetryAfter(tc.err); got != tc.want {
				t.Errorf("RetryAfter = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPollCountsElapsedFromTheOriginWhenOneIsGiven(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	var out bytes.Buffer
	check, _ := doneAfter(2, nil)

	err := Poll(context.Background(), Options{For: "build", Limit: time.Hour, Origin: clock.now.Add(-100 * time.Second), Out: &out, Clock: clock}, check)

	want := `{"v":1,"event":"waiting","elapsed_s":100,"for":"build"}` + "\n" +
		`{"v":1,"event":"waiting","elapsed_s":102,"for":"build"}` + "\n"
	if err != nil || out.String() != want {
		t.Errorf("err=%v out=%q, want nil and %q", err, out.String(), want)
	}
}

func TestPollKeepsItsLimitFromItsOwnStartWhenAnOriginIsGiven(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	check, _ := doneAfter(1000, nil)

	err := Poll(context.Background(), Options{Limit: 10 * time.Second, Origin: clock.now.Add(-100 * time.Second), Clock: clock}, check)

	var expired *ExpiredError
	if !errors.As(err, &expired) || clock.now != time.Unix(1010, 0) {
		t.Errorf("err=%v now=%v, want expiry exactly 10s after Poll began", err, clock.now)
	}
}

type brokenWriter struct{}

var errBrokenPipe = errors.New("broken pipe")

func (brokenWriter) Write([]byte) (int, error) { return 0, errBrokenPipe }

func TestPollStopsWhenItCanNoLongerWriteItsWaitingLines(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		clock := &fakeClock{}
		check, calls := doneAfter(1000, nil)

		err := Poll(context.Background(), Options{Limit: time.Hour, Out: brokenWriter{}, Pretty: pretty, Clock: clock}, check)

		if !errors.Is(err, errBrokenPipe) || *calls != 1 || len(clock.sleeps) != 0 {
			t.Errorf("pretty=%v: err=%v checks=%d sleeps=%v, want the write error after one check", pretty, err, *calls, clock.sleeps)
		}
	}
}

func TestPollGivesEachCheckOnlyTheRemainingBudget(t *testing.T) {
	check := func(ctx context.Context) (bool, time.Duration, error) {
		select {
		case <-ctx.Done():
			return false, 0, ctx.Err()
		case <-time.After(time.Second):
			return true, 0, nil
		}
	}
	began := time.Now()

	err := Poll(context.Background(), Options{For: "build", Limit: 50 * time.Millisecond}, check)

	var expired *ExpiredError
	if !errors.As(err, &expired) || time.Since(began) > 500*time.Millisecond {
		t.Errorf("err=%v after %s, want *ExpiredError within the 50ms budget", err, time.Since(began))
	}
}

func TestPollReportsACancelledContextAsCancelledNotExpired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Poll(ctx, Options{Limit: time.Hour}, func(ctx context.Context) (bool, time.Duration, error) {
		return false, 0, ctx.Err()
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v, want context.Canceled", err)
	}
}

func TestPollRunsTheCheckAtTheLimitWithAShortGraceRatherThanSkippingIt(t *testing.T) {
	clock := &fakeClock{}
	var budgets []time.Duration
	check := func(ctx context.Context) (bool, time.Duration, error) {
		deadline, _ := ctx.Deadline()
		budgets = append(budgets, time.Until(deadline).Round(time.Second))
		return len(budgets) == 2, 0, nil
	}

	err := Poll(context.Background(), Options{Limit: 2 * time.Second, Clock: clock}, check)

	if err != nil || len(budgets) != 2 || budgets[1] != FinalCheckGrace {
		t.Errorf("err=%v budgets=%v, want nil and a final check given the %s grace", err, budgets, FinalCheckGrace)
	}
}

func TestPollPassesThroughADeadlineErrorThatIsNotTheBudgets(t *testing.T) {
	stalled := fmt.Errorf("lookup: %w", context.DeadlineExceeded)

	err := Poll(context.Background(), Options{For: "build", Limit: time.Hour}, func(context.Context) (bool, time.Duration, error) {
		return false, 0, stalled
	})

	var expired *ExpiredError
	if errors.As(err, &expired) || !errors.Is(err, stalled) {
		t.Errorf("err=%v, want the check's own deadline error, not *ExpiredError", err)
	}
}
