package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func attemptsOf(t *testing.T, err error) (any, bool) {
	t.Helper()
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	n, ok := apiErr.Fields["attempts"]
	return n, ok
}

func TestRunningOutOfAttemptsReportsHowManyWereMade(t *testing.T) {
	useClock(t)
	client, _ := scripted(t, reply{status: 503})

	_, err := client.Me()

	if n, _ := attemptsOf(t, err); n != 3 || codeOf(t, err) != contract.ServerError {
		t.Errorf("attempts = %v code %q, want 3 and server_error", n, codeOf(t, err))
	}
}

func TestAnUnreachableServerReportsThreeAttempts(t *testing.T) {
	useClock(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	client := New(srv.URL, "tok")
	srv.Close()

	_, err := client.Me()

	if n, _ := attemptsOf(t, err); n != 3 || codeOf(t, err) != contract.NetworkError {
		t.Errorf("attempts = %v code %q, want 3 and network_error", n, codeOf(t, err))
	}
}

func TestAFailureAfterARetryReportsTwoAttempts(t *testing.T) {
	useClock(t)
	client, _ := scripted(t, reply{status: 503}, reply{status: 401})

	_, err := client.Me()

	if n, _ := attemptsOf(t, err); n != 2 || codeOf(t, err) != contract.Unauthorized {
		t.Errorf("attempts = %v code %q, want 2 and unauthorized", n, codeOf(t, err))
	}
}

func TestAFirstAttemptFailureCarriesNoAttempts(t *testing.T) {
	for _, status := range []int{401, 500, 404} {
		useClock(t)
		client, _ := scripted(t, reply{status: status})

		_, err := client.Me()

		if n, ok := attemptsOf(t, err); ok {
			t.Errorf("status %d: attempts = %v, want none after a single attempt", status, n)
		}
	}
}

func TestADeadlineThatBlocksTheFirstRetryCarriesNoAttempts(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{status: 503})
	ctx, cancel := context.WithDeadline(context.Background(), clock.now.Add(500*time.Millisecond))
	defer cancel()

	_, err := client.WithContext(ctx).Me()

	if n, ok := attemptsOf(t, err); ok || rec.count() != 1 {
		t.Errorf("attempts = %v after %d requests, want one request and no attempts", n, rec.count())
	}
}

func TestADeadlineThatCutsTheRetriesShortReportsTwoAttempts(t *testing.T) {
	clock := useClock(t)
	client, _ := scripted(t, reply{status: 503})
	ctx, cancel := context.WithDeadline(context.Background(), clock.now.Add(1500*time.Millisecond))
	defer cancel()

	_, err := client.WithContext(ctx).Me()

	if n, _ := attemptsOf(t, err); n != 2 {
		t.Errorf("attempts = %v, want 2", n)
	}
}

type cancelOnSecondWait struct {
	fakeClock
	waits int
}

func (c *cancelOnSecondWait) Sleep(ctx context.Context, d time.Duration) error {
	if c.waits++; c.waits == 2 {
		return context.Canceled
	}
	return c.fakeClock.Sleep(ctx, d)
}

func TestACancelledSecondWaitReportsTwoAttemptsAndTheCancel(t *testing.T) {
	t.Cleanup(SetClock(&cancelOnSecondWait{fakeClock: fakeClock{now: time.Now()}}))
	client, _ := scripted(t, reply{status: 503})

	_, err := client.Me()

	if n, _ := attemptsOf(t, err); n != 2 || !errors.Is(err, context.Canceled) {
		t.Errorf("attempts = %v err %v, want 2 and context.Canceled", n, err)
	}
}

func TestAContextThatExpiresDuringTheSecondAttemptReportsTwoAttempts(t *testing.T) {
	t.Cleanup(SetClock(&fakeClock{}))
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := New(srv.URL, "tok").WithContext(ctx).Me()

	if n, _ := attemptsOf(t, err); n != 2 || !errors.Is(err, context.DeadlineExceeded) || hits.Load() != 2 {
		t.Errorf("attempts = %v err %v after %d requests, want 2 and the deadline after 2", n, err, hits.Load())
	}
}
