package approvals

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/wait"
)

type fakeClock struct {
	now     time.Time
	sleeps  []time.Duration
	onSleep func()
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	if c.onSleep != nil {
		c.onSleep()
	}
	return ctx.Err()
}

type reply struct {
	status     int
	body       string
	retryAfter string
}

func nullable(s string) string {
	if s == "" {
		return "null"
	}
	return strconv.Quote(s)
}

func approvalJSON(status, decidedAt, note string) string {
	return `{"approval":{"id":"a1","url":"https://selective.example/a1","status":` + strconv.Quote(status) + `,"action":"suite.delete","subject":{"type":"suite","id":"s1","label":null},"params":{},"requested_by":{"token_id":null,"token_name":null,"user_id":null},"expires_at":null,"decided_at":` + nullable(decidedAt) + `,"decided_by_user_id":null,"decision_note":` + nullable(note) + `,"consumed_at":null,"inserted_at":"2026-11-14T10:00:00Z"}}`
}

func serve(t *testing.T, replies ...reply) *api.Client {
	t.Helper()
	t.Cleanup(api.SetClock(&fakeClock{}))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rep := replies[min(calls, len(replies)-1)]
		calls++
		if rep.retryAfter != "" {
			w.Header().Set("Retry-After", rep.retryAfter)
		}
		w.WriteHeader(rep.status)
		fmt.Fprint(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return api.New(srv.URL, "tok")
}

func ok(status string) reply { return decided(status, "", "") }

func decided(status, decidedAt, note string) reply {
	return reply{status: 200, body: approvalJSON(status, decidedAt, note)}
}

var (
	unavailable = reply{status: 503, body: `{"error":"server_error","message":"down"}`}
	limited     = reply{status: 429, body: `{"error":"rate_limited","message":"slow"}`, retryAfter: "7"}
)

func exitOf(t *testing.T, err error) int {
	t.Helper()
	var coded contract.Coded
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v, want a coded error", err)
	}
	return coded.ExitCode()
}

func TestAwaitWaitsOutARateLimitForTheRetryAfterItSent(t *testing.T) {
	clock := &fakeClock{}

	a, err := Await(context.Background(), serve(t, ok("pending"), limited, ok("approved")), "a1", wait.Options{Limit: time.Hour, Clock: clock})

	if err != nil || a.Status != "approved" {
		t.Fatalf("Await = %v, %v, want approved", a, err)
	}
	if want := []time.Duration{2 * time.Second, 7 * time.Second}; !slices.Equal(clock.sleeps, want) {
		t.Errorf("sleeps = %v, want %v", clock.sleeps, want)
	}
}

func TestAwaitReturnsTheTransientErrorWhenTheWaitEndsOnIt(t *testing.T) {
	var out bytes.Buffer

	_, err := Await(context.Background(), serve(t, ok("pending"), unavailable), "a1", wait.Options{Limit: 5 * time.Second, Out: &out, Clock: &fakeClock{}})

	var apiErr *api.Error
	if !errors.As(err, &apiErr) || exitOf(t, err) != 4 || apiErr.Fields["attempts"] != 3 {
		t.Errorf("err = %v, want exit 4 server_error with attempts 3", err)
	}
}

func TestAwaitReportsTheTimeoutCauseEvenWhenTheLastPollWasTransient(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	clock := &fakeClock{onSleep: func() { cancel(contract.Errorf(contract.Timeout, "--timeout expired")) }}

	_, err := Await(ctx, serve(t, unavailable), "a1", wait.Options{Limit: time.Hour, Clock: clock})

	if exitOf(t, err) != 8 {
		t.Errorf("err = %v, want exit 8 timeout", err)
	}
}

func TestAwaitNeverReportsAnUnknownStatusAsApproved(t *testing.T) {
	_, err := Await(context.Background(), serve(t, ok("revoked")), "a1", wait.Options{Limit: time.Hour, Clock: &fakeClock{}})

	if exitOf(t, err) != contract.ExitInternal {
		t.Errorf("err = %v, want internal_error", err)
	}
}

func TestAwaitEndsWithTheOutcomeAndCarriesTheApprovalInEveryError(t *testing.T) {
	cases := []struct {
		name    string
		replies []reply
		limit   time.Duration
		exit    int
		message string
	}{
		{"denied with a note", []reply{ok("pending"), decided("denied", "2026-11-14T11:00:00Z", "wrong suite")}, time.Hour, 6, "approval denied: wrong suite"},
		{"denied without a note", []reply{decided("denied", "2026-11-14T11:00:00Z", "")}, time.Hour, 6, "approval denied"},
		{"pending lapsed", []reply{ok("pending"), ok("expired")}, time.Hour, 7, "approval expired before anyone decided it; request the action again"},
		{"approved but never used", []reply{decided("expired", "2026-11-14T11:00:00Z", "")}, time.Hour, 7, "approval was approved at 2026-11-14T11:00:00Z but not used before it expired; request the action again"},
		{"wait ran out", []reply{ok("pending")}, 5 * time.Second, 7, "still pending after 5s; a person must decide it at https://selective.example/a1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Await(context.Background(), serve(t, tc.replies...), "a1", wait.Options{Limit: tc.limit, Clock: &fakeClock{}})

			var fielded contract.Fielded
			if !errors.As(err, &fielded) {
				t.Fatalf("err = %v, want an error with fields", err)
			}
			carried, _ := fielded.ErrorFields()["approval"].(*api.Approval)
			if exitOf(t, err) != tc.exit || err.Error() != tc.message || carried == nil || carried.ID != "a1" {
				t.Errorf("exit %d message %q approval %v; want exit %d, message %q and the approval", exitOf(t, err), err.Error(), carried, tc.exit, tc.message)
			}
		})
	}
}
