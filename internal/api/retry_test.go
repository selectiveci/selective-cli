package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
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

type reply struct{ status int }

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
