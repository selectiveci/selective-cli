package api

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/wait"
)

const (
	maxAttempts = 3
	baseBackoff = time.Second
	jitter      = 0.2

	maxRetryAfter = 10 * time.Second
)

func transient(err error) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case 0:
		return apiErr.Code == contract.NetworkError
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *Client) declinesRetry(err error) bool {
	var apiErr *Error
	return c.noRateLimitRetry && errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests
}

func advisedWait(err error) (time.Duration, bool) {
	var apiErr *Error
	if !errors.As(err, &apiErr) || (apiErr.Status != http.StatusTooManyRequests && apiErr.Status != http.StatusServiceUnavailable) {
		return 0, false
	}
	var seconds float64
	switch v := apiErr.Fields["retry_after_s"].(type) {
	case int:
		seconds = float64(v)
	case float64:
		seconds = v
	default:
		return 0, false
	}
	switch {
	case seconds < 0:
		return 0, false
	case seconds > maxRetryAfter.Seconds():
		return math.MaxInt64, true
	}
	return time.Duration(seconds * float64(time.Second)), true
}

func backoff(attempt int, err error) time.Duration {
	if advised, ok := advisedWait(err); ok {
		return advised
	}
	base := baseBackoff << (attempt - 1)
	return time.Duration(float64(base) * (1 + jitter*(2*rand.Float64()-1)))
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

var clock atomic.Pointer[wait.Clock]

func SetClock(c wait.Clock) (restore func()) {
	previous := clock.Swap(&c)
	return func() { clock.Store(previous) }
}

func currentClock() wait.Clock {
	if c := clock.Load(); c != nil {
		return *c
	}
	return realClock{}
}
