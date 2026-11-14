package tail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/wait"
)

type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func lookupServer(t *testing.T, replies ...func(http.ResponseWriter)) (*api.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		replies[min(n, len(replies))-1](w)
	}))
	t.Cleanup(srv.Close)
	return api.New(srv.URL, "tok"), &calls
}

func rateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "7")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"rate_limited","message":"slow down"}`))
}

func found(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"build_id":"b9"}`)) }

func TestFindBuildWaitsOutARateLimitForTheRetryAfterItSent(t *testing.T) {
	client, calls := lookupServer(t, rateLimited, found)
	clock := &fakeClock{}

	id, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: time.Hour, Clock: clock})

	if err != nil || id != "b9" {
		t.Fatalf("findBuild = %q, %v, want b9", id, err)
	}
	if want := []time.Duration{7 * time.Second}; !slices.Equal(clock.sleeps, want) || calls.Load() != 2 {
		t.Errorf("sleeps = %v after %d lookups, want [7s] after 2", clock.sleeps, calls.Load())
	}
}

func TestFindBuildStopsPollingOnAnErrorThatIsNotAMissingBuild(t *testing.T) {
	unauthorized := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}
	client, calls := lookupServer(t, unauthorized)
	clock := &fakeClock{}

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: time.Hour, Clock: clock})

	var coded contract.Coded
	if !errors.As(err, &coded) || coded.ErrorCode() != contract.Unauthorized {
		t.Errorf("err = %v, want unauthorized", err)
	}
	if calls.Load() != 1 || len(clock.sleeps) != 0 {
		t.Errorf("lookups = %d sleeps = %v, want one lookup and no sleep", calls.Load(), clock.sleeps)
	}
}

func TestFindBuildWithoutAWaitSurfacesARateLimitInsteadOfNoBuild(t *testing.T) {
	client, calls := lookupServer(t, rateLimited)

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{})

	var coded contract.Coded
	if !errors.As(err, &coded) || coded.ErrorCode() != contract.RateLimited || calls.Load() != 3 {
		t.Errorf("err = %v after %d lookups, want rate_limited after the client's 3 attempts", err, calls.Load())
	}
}

func TestFindBuildTreatsAnyTooManyRequestsStatusAsARateLimit(t *testing.T) {
	proxyLimit := func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"too_many_requests"}`))
	}
	client, calls := lookupServer(t, proxyLimit, found)
	clock := &fakeClock{}

	id, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: time.Hour, Clock: clock})

	if err != nil || id != "b9" || !slices.Equal(clock.sleeps, []time.Duration{7 * time.Second}) || calls.Load() != 2 {
		t.Errorf("id=%q err=%v sleeps=%v lookups=%d, want b9 after one 7s wait", id, err, clock.sleeps, calls.Load())
	}
}

func TestFindBuildReportsAStalledLookupAsANetworkFailureNotAMissingBuild(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)
	client := api.New(stalled.URL, "tok")
	client.HTTP.Timeout = 100 * time.Millisecond
	began := time.Now()

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: time.Hour})

	var coded contract.Coded
	if !errors.As(err, &coded) || coded.ErrorCode() != contract.NetworkError || time.Since(began) > 5*time.Second {
		t.Errorf("err = %v after %s, want network_error, not no_build", err, time.Since(began))
	}
}
