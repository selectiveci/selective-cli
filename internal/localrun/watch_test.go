package localrun

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type edit struct {
	at    time.Duration
	value string
}

type watchWorld struct {
	now     time.Duration
	edits   []edit
	runTook time.Duration
	stopAt  time.Duration
	cancel  context.CancelFunc
	ranAt   []time.Duration
	killed  bool
}

func (w *watchWorld) fingerprint(string) (string, error) {
	value := "v0"
	for _, e := range w.edits {
		if e.at <= w.now {
			value = e.value
		}
	}
	return value, nil
}

func (w *watchWorld) sleep(ctx context.Context, d time.Duration) error {
	w.now += d
	if w.now >= w.stopAt {
		w.cancel()
	}
	return ctx.Err()
}

func (w *watchWorld) iterate(ctx context.Context, n int) (Summary, error) {
	w.ranAt = append(w.ranAt, w.now)
	w.now += w.runTook
	w.killed = w.killed || ctx.Err() != nil
	return Summary{Passed: n}, nil
}

func (w *watchWorld) watch(t *testing.T) (Summary, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.cancel = cancel
	w.stopAt = cmpDuration(w.stopAt, 10*time.Second)
	return Watch{Dir: "unused", Sleep: w.sleep, Fingerprint: w.fingerprint}.Run(ctx, w.iterate)
}

func cmpDuration(got, fallback time.Duration) time.Duration {
	if got == 0 {
		return fallback
	}
	return got
}

func TestWatchRunsOnceAtStartAndNothingElseWhileNothingChanges(t *testing.T) {
	world := &watchWorld{}

	sum, err := world.watch(t)

	if !errors.Is(err, ErrInterrupted) || !slices.Equal(world.ranAt, []time.Duration{0}) || sum.Passed != 1 {
		t.Errorf("ran at %v, summary %+v, err %v; want one run at 0, the last iteration's counts, and ErrInterrupted", world.ranAt, sum, err)
	}
}

func TestWatchCoalescesTwoWritesWithinTheDebounceIntoOneRun(t *testing.T) {
	world := &watchWorld{edits: []edit{{1000 * time.Millisecond, "a"}, {1250 * time.Millisecond, "b"}}}

	_, _ = world.watch(t)

	if len(world.ranAt) != 2 {
		t.Fatalf("ran at %v, want the first run and exactly one more for two writes 250ms apart", world.ranAt)
	}
	if settled := 1250*time.Millisecond + WatchDebounce; world.ranAt[1] < settled {
		t.Errorf("second run at %v, want it no earlier than %v (300ms after the last write)", world.ranAt[1], settled)
	}
}

func TestWatchRunsAgainForWritesFurtherApartThanTheDebounce(t *testing.T) {
	world := &watchWorld{edits: []edit{{1000 * time.Millisecond, "a"}, {3000 * time.Millisecond, "b"}}}

	_, _ = world.watch(t)

	if len(world.ranAt) != 3 {
		t.Errorf("ran at %v, want a run per burst of writes", world.ranAt)
	}
}

func TestWatchQueuesExactlyOneRerunForChangesDuringARunAndDoesNotKillIt(t *testing.T) {
	world := &watchWorld{runTook: 600 * time.Millisecond, edits: []edit{{100 * time.Millisecond, "a"}, {300 * time.Millisecond, "b"}}}

	_, _ = world.watch(t)

	if len(world.ranAt) != 2 {
		t.Errorf("ran at %v, want the first run and exactly one queued rerun for two mid-run edits", world.ranAt)
	}
	if world.killed {
		t.Error("a run saw its context cancelled by a change; want it left to finish")
	}
	if len(world.ranAt) > 1 && world.ranAt[1] < 600*time.Millisecond {
		t.Errorf("rerun started at %v, before the first run ended at 600ms", world.ranAt[1])
	}
}

func TestWatchReturnsTheInterruptedRunsCountsWhenSignalledMidRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch := Watch{Dir: "unused", Sleep: func(context.Context, time.Duration) error { return nil }, Fingerprint: func(string) (string, error) { return "same", nil }}

	sum, err := watch.Run(ctx, func(ctx context.Context, n int) (Summary, error) {
		cancel()
		return Summary{Passed: 4, Selected: 9}, ErrInterrupted
	})

	if !errors.Is(err, ErrInterrupted) || sum.Passed != 4 || sum.Selected != 9 {
		t.Errorf("summary %+v err %v, want the counts so far and ErrInterrupted", sum, err)
	}
}

func TestWatchEndsOnAnIterationError(t *testing.T) {
	boom := errors.New("boom")
	watch := Watch{Dir: "unused", Sleep: func(context.Context, time.Duration) error { return nil }, Fingerprint: func(string) (string, error) { return "same", nil }}

	_, err := watch.Run(context.Background(), func(context.Context, int) (Summary, error) { return Summary{}, boom })

	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the iteration's error to end the watch", err)
	}
}

func TestWatchChangeDetectionUsesTheGitSnapshotSoGitignoredEditsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	write(t, dir, ".gitignore", "ignored.log\n")
	write(t, dir, "tracked.txt", "v1\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	write(t, dir, "ignored.log", "v1\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs, step := 0, 0
	var runsAfterIgnoredEdit int
	sleep := func(ctx context.Context, _ time.Duration) error {
		step++
		switch step {
		case 1:
			write(t, dir, "ignored.log", "v2\n")
		case 2:
			runsAfterIgnoredEdit = runs
			write(t, dir, "tracked.txt", "v2\n")
		}
		return ctx.Err()
	}

	_, err := Watch{Dir: dir, Sleep: sleep}.Run(ctx, func(context.Context, int) (Summary, error) {
		runs++
		if runs == 2 {
			cancel()
		}
		return Summary{}, nil
	})

	if !errors.Is(err, ErrInterrupted) || runsAfterIgnoredEdit != 1 || runs != 2 {
		t.Errorf("runs %d (%d after the gitignored edit), err %v; want the gitignored edit to trigger nothing and the tracked edit one run", runs, runsAfterIgnoredEdit, err)
	}
}

func TestWatchRefusesADirectoryThatIsNotACheckout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", dir)

	_, err := Watch{Dir: dir}.Run(context.Background(), func(context.Context, int) (Summary, error) { return Summary{}, nil })

	if err == nil || errors.Is(err, ErrInterrupted) {
		t.Errorf("err = %v, want the snapshot failure", err)
	}
}
