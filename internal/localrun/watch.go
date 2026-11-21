package localrun

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"time"
)

const (
	WatchPoll     = 300 * time.Millisecond
	WatchDebounce = 300 * time.Millisecond
)

type Watch struct {
	Dir         string
	Poll        time.Duration
	Debounce    time.Duration
	Sleep       func(context.Context, time.Duration) error
	Fingerprint func(dir string) (string, error)
}

type Iteration func(ctx context.Context, n int) (Summary, error)

func (w Watch) Run(ctx context.Context, iterate Iteration) (Summary, error) {
	fingerprint := w.Fingerprint
	if fingerprint == nil {
		fingerprint = func(dir string) (string, error) { return treeFingerprint(ctx, dir) }
	}
	seen, err := fingerprint(w.Dir)
	if err != nil {
		return Summary{}, err
	}
	probe := func(previous string) string {
		if current, err := fingerprint(w.Dir); err == nil {
			return current
		}
		return previous
	}
	pause := func(d time.Duration) error {
		if w.Sleep == nil {
			return sleepFor(ctx, d)
		}
		return w.Sleep(ctx, d)
	}
	var sum Summary
	for n := 1; ; n++ {
		if ctx.Err() != nil {
			return sum, ErrInterrupted
		}
		if sum, err = iterate(ctx, n); err != nil {
			return sum, err
		}
		current := probe(seen)
		for current == seen {
			if pause(cmp.Or(w.Poll, WatchPoll)) != nil {
				return sum, ErrInterrupted
			}
			current = probe(seen)
		}
		for settled := false; !settled; {
			if pause(cmp.Or(w.Debounce, WatchDebounce)) != nil {
				return sum, ErrInterrupted
			}
			next, err := fingerprint(w.Dir)
			if err != nil {
				continue
			}
			settled, current = next == current, next
		}
		seen = current
	}
}

func sleepFor(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func treeFingerprint(ctx context.Context, dir string) (string, error) {
	snapshot, err := takeSnapshot(ctx, dir)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, path := range slices.Sorted(maps.Keys(snapshot.Files)) {
		fmt.Fprintf(digest, "%s\x00%s\x00%s\n", path, snapshot.Files[path], snapshot.modes[path])
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
