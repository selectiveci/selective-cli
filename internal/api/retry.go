package api

import (
	"context"
	"errors"
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

func withAttempts(err error, attempts int) error {
	var apiErr *Error
	if attempts < 2 || !errors.As(err, &apiErr) {
		return err
	}
	if apiErr.Fields == nil {
		apiErr.Fields = map[string]any{}
	}
	apiErr.Fields["attempts"] = attempts
	return err
}

func (c *Client) declinesRetry(err error) bool {
	var apiErr *Error
	return c.noRateLimitRetry && errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests
}

func backoff(attempt int, err error) time.Duration {
	var apiErr *Error
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusTooManyRequests || apiErr.Status == http.StatusServiceUnavailable) {
		if advised := wait.RetryAfter(err); advised > 0 {
			return advised
		}
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
