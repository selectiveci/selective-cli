package tail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	t.Cleanup(api.SetClock(&fakeClock{}))
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

func assertRefused(t *testing.T, err error, code contract.Code, exit int) map[string]any {
	t.Helper()
	got, _ := refuse(err)
	var coded contract.Coded
	if !errors.As(err, &coded) || coded.ErrorCode() != code || got != exit {
		t.Fatalf("err = %v exit %d, want %s exit %d", err, got, code, exit)
	}
	var fielded contract.Fielded
	if errors.As(err, &fielded) {
		return fielded.ErrorFields()
	}
	return nil
}

func TestFindBuildReportsAStalledLookupAsANetworkFailureNotAMissingBuildWhenTheWaitExpires(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)
	client := api.New(stalled.URL, "tok")
	client.HTTP.Timeout = 100 * time.Millisecond

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: 250 * time.Millisecond})

	fields := assertRefused(t, err, contract.NetworkError, contract.ExitRetryable)
	if attempts, _ := fields["attempts"].(int); attempts < 2 {
		t.Errorf("fields = %v, want attempts of at least 2", fields)
	}
}

func TestFindBuildRidesOutAServerErrorBurstLongerThanTheClientsAttempts(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	unavailable := func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
	client, calls := lookupServer(t, unavailable, unavailable, unavailable, unavailable, unavailable, found)
	clock := &fakeClock{}

	id, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: time.Hour, Clock: clock})

	if err != nil || id != "b9" || calls.Load() != 6 || !slices.Equal(clock.sleeps, []time.Duration{2 * time.Second}) {
		t.Errorf("id=%q err=%v lookups=%d sleeps=%v, want b9 after 6 lookups and one 2s wait", id, err, calls.Load(), clock.sleeps)
	}
}

func TestFindBuildExpiresAsTheTransientErrorWhenTheLastLookupFailedTransiently(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	unavailable := func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
	client, calls := lookupServer(t, unavailable)

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: 10 * time.Minute, Clock: &fakeClock{}})

	fields := assertRefused(t, err, contract.ServerError, contract.ExitRetryable)
	if fields["attempts"] != 3 || calls.Load() <= 3 {
		t.Errorf("fields = %v after %d lookups, want attempts 3 and polling past the first poll", fields, calls.Load())
	}
}

func TestFindBuildExpiresAsNoBuildWhenTheLastLookupWasAMissingBuild(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	unavailable := func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
	missing := func(w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) }
	client, _ := lookupServer(t, unavailable, unavailable, unavailable, missing)

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: time.Minute, Clock: &fakeClock{}})

	assertRefused(t, err, contract.NoBuild, contract.ExitNotFound)
}

func TestFindBuildExpiresAsRateLimitedWhenTheLastLookupWasRateLimited(t *testing.T) {
	client, _ := lookupServer(t, rateLimited)

	_, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{Limit: 20 * time.Second, Clock: &fakeClock{}})

	fields := assertRefused(t, err, contract.RateLimited, contract.ExitRetryable)
	if fields["retry_after_s"] != 7 {
		t.Errorf("fields = %v, want retry_after_s 7", fields)
	}
}

func inGitRepoWithOrigin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/acme/widget.git"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestRunTimeoutAfterARetriedLookupExitsEightWithTheAttempts(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	inGitRepoWithOrigin(t)
	var lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lookups.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	code, err := Run(api.New(srv.URL, "tok"), Options{Commit: "abc1234", Format: "ndjson", Timeout: 200 * time.Millisecond})

	fields := assertRefused(t, err, contract.Timeout, contract.ExitTimeout)
	if code != contract.ExitTimeout || fields["attempts"] != 2 || lookups.Load() != 2 {
		t.Errorf("exit %d fields %v after %d lookups, want exit 8 with attempts 2 after 2 lookups", code, fields, lookups.Load())
	}
}
