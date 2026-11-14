package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func TestMain(m *testing.M) {
	SetClock(&fakeClock{now: time.Now()})
	os.Exit(m.Run())
}

func useClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{now: time.Now()}
	t.Cleanup(SetClock(c))
	return c
}

type reply struct {
	status  int
	headers map[string]string
}

type seen struct {
	key  string
	body string
}

type recorder struct {
	mu   sync.Mutex
	reqs []seen
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func scripted(t *testing.T, replies ...reply) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, seen{r.Header.Get("Idempotency-Key"), string(body)})
		n := len(rec.reqs)
		rec.mu.Unlock()
		rp := replies[min(n, len(replies))-1]
		for k, v := range rp.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rp.status)
		_, _ = w.Write([]byte(`{"user_id":"u1"}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok"), rec
}

func codeOf(t *testing.T, err error) contract.Code {
	t.Helper()
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	return apiErr.Code
}

func within(d time.Duration, want time.Duration) bool {
	return d >= want*8/10 && d <= want*12/10
}

func TestA503ThenSuccessRetriesWithOneThenTwoSeconds(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{status: 503}, reply{status: 503}, reply{status: 200})

	me, err := client.Me()

	if err != nil || me.UserID != "u1" || rec.count() != 3 {
		t.Fatalf("me %+v err %v after %d requests, want success on the third", me, err, rec.count())
	}
	if len(clock.sleeps) != 2 || !within(clock.sleeps[0], time.Second) || !within(clock.sleeps[1], 2*time.Second) {
		t.Errorf("sleeps = %v, want ~1s then ~2s", clock.sleeps)
	}
}

func TestA503ThatNeverClearsStopsAtThreeRequests(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{status: 503})

	_, err := client.Me()

	if code := codeOf(t, err); code != contract.ServerError || rec.count() != 3 || len(clock.sleeps) != 2 {
		t.Errorf("code %q after %d requests and %d waits, want server_error after 3 and 2", code, rec.count(), len(clock.sleeps))
	}
}

func TestEveryRetryableStatusIsRetried(t *testing.T) {
	for _, status := range []int{502, 503, 504, 429} {
		client, rec := scripted(t, reply{status: status}, reply{status: 200})

		if _, err := client.Me(); err != nil || rec.count() != 2 {
			t.Errorf("status %d: err %v after %d requests, want success on the second", status, err, rec.count())
		}
	}
}

func TestStatusesThatNeverImproveAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 409, 422, 500} {
		client, rec := scripted(t, reply{status: status}, reply{status: 200})

		_, err := client.Me()

		if err == nil || rec.count() != 1 {
			t.Errorf("status %d: err %v after %d requests, want the error after 1", status, err, rec.count())
		}
	}
}

func TestAnUndecodableSuccessIsNotRetried(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{status: 200})
	var out chan int

	err := client.do("GET", "/api/v1/me", nil, http.StatusOK, &out)

	if err == nil || rec.count() != 1 || len(clock.sleeps) != 0 {
		t.Errorf("err %v after %d requests and %d waits, want 1 and 0", err, rec.count(), len(clock.sleeps))
	}
}

func TestA503ThenA401ReturnsTheUnauthorized(t *testing.T) {
	useClock(t)
	client, rec := scripted(t, reply{status: 503}, reply{status: 401})

	_, err := client.Me()

	if code := codeOf(t, err); code != contract.Unauthorized || rec.count() != 2 {
		t.Errorf("code %q after %d requests, want unauthorized after 2", code, rec.count())
	}
}

func TestRetryAfterReplacesTheBackoff(t *testing.T) {
	for _, status := range []int{429, 503} {
		clock := useClock(t)
		client, _ := scripted(t, reply{status, map[string]string{"Retry-After": "5"}}, reply{status: 200})

		if _, err := client.Me(); err != nil || len(clock.sleeps) != 1 || clock.sleeps[0] != 5*time.Second {
			t.Errorf("status %d: err %v sleeps %v, want one 5s wait", status, err, clock.sleeps)
		}
	}
}

func TestARetryAfterOfTenSecondsIsWaitedOut(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{429, map[string]string{"Retry-After": "10"}}, reply{status: 200})

	_, err := client.Me()

	if err != nil || rec.count() != 2 || len(clock.sleeps) != 1 || clock.sleeps[0] != 10*time.Second {
		t.Errorf("err %v after %d requests, sleeps %v, want success on the second after one 10s wait", err, rec.count(), clock.sleeps)
	}
}

func TestARetryAfterOverTenSecondsReturnsAtOnceWithTheAdvice(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{429, map[string]string{"Retry-After": "11"}}, reply{status: 200})

	_, err := client.Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != contract.RateLimited || apiErr.Fields["retry_after_s"] != 11 || rec.count() != 1 || len(clock.sleeps) != 0 {
		t.Errorf("err %v after %d requests, sleeps %v, want rate_limited with retry_after_s 11 after 1 and no wait", err, rec.count(), clock.sleeps)
	}
}

func TestAnUnreachableServerIsRetriedTwice(t *testing.T) {
	clock := useClock(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	client := New(srv.URL, "tok")
	srv.Close()

	_, err := client.Me()

	if code := codeOf(t, err); code != contract.NetworkError || len(clock.sleeps) != 2 {
		t.Errorf("code %q after %d waits, want network_error after 2", code, len(clock.sleeps))
	}
}

func TestARequestTimeoutIsRetried(t *testing.T) {
	useClock(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"user_id":"u1"}`))
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, "tok")
	client.HTTP.Timeout = 100 * time.Millisecond

	me, err := client.Me()

	if err != nil || me.UserID != "u1" || hits.Load() != 2 {
		t.Errorf("me %+v err %v after %d requests, want success on the second", me, err, hits.Load())
	}
}

func TestAContextThatExpiresMidRequestIsNotRetried(t *testing.T) {
	clock := useClock(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := New(srv.URL, "tok").WithContext(ctx).Me()

	if !errors.Is(err, context.DeadlineExceeded) || hits.Load() != 1 || len(clock.sleeps) != 0 {
		t.Errorf("err %v after %d requests and %d waits, want the deadline after 1 and 0", err, hits.Load(), len(clock.sleeps))
	}
}

func TestWritesAreRetriedOnlyWithAnIdempotencyKey(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		clock := useClock(t)
		client, rec := scripted(t, reply{status: 503}, reply{status: 200})

		err := client.doWith(nil, method, "/api/v1/x", map[string]string{"a": "b"}, http.StatusOK, nil)

		if codeOf(t, err) != contract.ServerError || rec.count() != 1 || len(clock.sleeps) != 0 {
			t.Errorf("%s without a key: err %v after %d requests, want the 503 after 1", method, err, rec.count())
		}
	}
}

func TestAKeyedWriteIsRetriedWithTheSameKeyAndBody(t *testing.T) {
	useClock(t)
	client, rec := scripted(t, reply{status: 503}, reply{status: 200})
	header := http.Header{"Idempotency-Key": {"k-1"}}

	err := client.doWith(header, "POST", "/api/v1/x", map[string]string{"name": "n"}, http.StatusOK, nil)

	want := seen{"k-1", `{"name":"n"}`}
	if err != nil || len(rec.reqs) != 2 || rec.reqs[0] != want || rec.reqs[1] != want {
		t.Errorf("err %v requests %+v, want two identical requests %+v", err, rec.reqs, want)
	}
}

func TestADeadlineStopsTheRetriesBeforeAWaitThatWouldPassIt(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{status: 503})
	deadline := clock.now.Add(1500 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	_, err := client.WithContext(ctx).Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.ExitCode() != 4 || rec.count() != 2 || len(clock.sleeps) != 1 {
		t.Fatalf("err %v after %d requests and %d waits, want exit 4 after 2 and 1", err, rec.count(), len(clock.sleeps))
	}
	if clock.now.After(deadline) {
		t.Errorf("the clock reached %v, past the deadline %v", clock.now, deadline)
	}
}

func TestCancellingDuringAWaitReturnsAtOnce(t *testing.T) {
	t.Cleanup(SetClock(realClock{}))
	client, rec := scripted(t, reply{status: 503})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()

	_, err := client.WithContext(ctx).Me()

	if elapsed := time.Since(start); elapsed > 150*time.Millisecond || !errors.Is(err, context.Canceled) || rec.count() != 1 {
		t.Errorf("err %v after %v and %d requests, want context.Canceled right after the cancel", err, elapsed, rec.count())
	}
}

type countingBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (b countingBody) Close() error {
	b.closed.Add(1)
	return b.ReadCloser.Close()
}

type countingTransport struct{ opened, closed atomic.Int32 }

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		c.opened.Add(1)
		resp.Body = countingBody{resp.Body, &c.closed}
	}
	return resp, err
}

func TestEveryFailedAttemptsBodyIsClosed(t *testing.T) {
	useClock(t)
	client, _ := scripted(t, reply{status: 503})
	transport := &countingTransport{}
	client.HTTP.Transport = transport

	_, _ = client.Me()

	if transport.opened.Load() != 3 || transport.closed.Load() != 3 {
		t.Errorf("opened %d bodies and closed %d, want 3 and 3", transport.opened.Load(), transport.closed.Load())
	}
}

func TestAPollingClientStillRetriesA503(t *testing.T) {
	useClock(t)
	client, rec := scripted(t, reply{status: 503}, reply{status: 503}, reply{status: 200})

	_, err := client.WithoutRateLimitRetry().Me()

	if err != nil || rec.count() != 3 {
		t.Errorf("err %v after %d requests, want success on the third", err, rec.count())
	}
}

func TestAPollingClientDoesNotRetryA429(t *testing.T) {
	clock := useClock(t)
	client, rec := scripted(t, reply{status: 429}, reply{status: 200})

	_, err := client.WithoutRateLimitRetry().Me()

	if codeOf(t, err) != contract.RateLimited || rec.count() != 1 || len(clock.sleeps) != 0 {
		t.Errorf("err %v after %d requests and %d waits, want the 429 after 1 and 0", err, rec.count(), len(clock.sleeps))
	}
}

func TestWithoutRateLimitRetryLeavesTheOriginalClientRetrying(t *testing.T) {
	useClock(t)
	client, rec := scripted(t, reply{status: 503}, reply{status: 200})
	client.WithoutRateLimitRetry()

	if _, err := client.Me(); err != nil || rec.count() != 2 {
		t.Errorf("err %v after %d requests, want success on the second", err, rec.count())
	}
}
