package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/schema"
	"github.com/spf13/cobra"
)

const (
	pendingA1  = `{"result":"pending_approval","dry_run":false,"approval":{"id":"a1","url":"https://selective.example/a1","status":"pending","expires_at":"2026-11-14T11:00:00Z"}}`
	pendingA2  = `{"result":"pending_approval","dry_run":false,"approval":{"id":"a2","url":"https://selective.example/a2","status":"pending","expires_at":"2026-11-14T12:00:00Z"}}`
	pendingDry = `{"result":"pending_approval","dry_run":true}`
	deleted    = `{"result":"deleted","dry_run":false}`
)

type call struct {
	method, path, body string
	header             http.Header
}

type gateServer struct {
	*httptest.Server
	mu        sync.Mutex
	writes    []call
	polls     int
	writeBody []reply
	approvals []reply
}

func newGateServer(t *testing.T, writes []reply, approvals ...reply) *gateServer {
	t.Helper()
	t.Cleanup(api.SetClock(&fakeClock{}))
	g := &gateServer{writeBody: writes, approvals: approvals}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		var rep reply
		if strings.HasPrefix(r.URL.Path, "/api/v1/approvals/") {
			rep = g.approvals[min(g.polls, len(g.approvals)-1)]
			g.polls++
		} else {
			body, _ := io.ReadAll(r.Body)
			g.writes = append(g.writes, call{r.Method, r.URL.Path, string(body), r.Header.Clone()})
			rep = g.writeBody[min(len(g.writes)-1, len(g.writeBody)-1)]
		}
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gateServer) sendDelete(key string) func(context.Context) (*Outcome, error) {
	return func(ctx context.Context) (*Outcome, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, g.URL+"/api/v1/suites/s1", strings.NewReader(`{"reason":"cleanup"}`))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var wire struct {
			Result   string          `json:"result"`
			DryRun   bool            `json:"dry_run"`
			Approval json.RawMessage `json:"approval"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
			return nil, err
		}
		if wire.Result == "pending_approval" {
			return nil, &api.PendingApproval{DryRun: wire.DryRun, Approval: wire.Approval}
		}
		return &Outcome{Key: "suite", Resource: map[string]string{"id": "s1"}, Result: wire.Result, DryRun: wire.DryRun}, nil
	}
}

func (g *gateServer) gate(out, errOut io.Writer) *Gate {
	return &Gate{Client: api.New(g.URL, "tok"), Out: out, Err: errOut, JSON: true, Wait: time.Hour, Clock: &fakeClock{}}
}

func (g *gateServer) writeCalls() []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]call(nil), g.writes...)
}

func (g *gateServer) pollCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.polls
}

func sentence(o *Outcome) string { return "Suite " + o.Result }

func codeOf(t *testing.T, err error) (contract.Code, int) {
	t.Helper()
	var coded contract.Coded
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v, want a coded error", err)
	}
	return coded.ErrorCode(), coded.ExitCode()
}

func TestGateResendsOnceAfterApprovalWithTheSameRequest(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}, {200, deleted, ""}}, ok("pending"), ok("approved"))
	var out bytes.Buffer

	err := srv.gate(&out, io.Discard).Do(context.Background(), srv.sendDelete("key-1"), sentence)

	want := `{"v":1,"result":"pending_approval","dry_run":false,"approval":{"id":"a1","url":"https://selective.example/a1","status":"pending","expires_at":"2026-11-14T11:00:00Z"}}` + "\n" +
		`{"v":1,"event":"waiting","elapsed_s":0,"for":"approval"}` + "\n" +
		`{"v":1,"event":"done","result":"deleted","suite":{"id":"s1"}}` + "\n"
	calls := srv.writeCalls()
	if err != nil || out.String() != want {
		t.Errorf("err = %v, output:\n%s\nwant:\n%s", err, out.String(), want)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], calls[1]) {
		t.Fatalf("write calls = %+v, want two identical requests", calls)
	}
	if got := calls[0]; got.method != "DELETE" || got.path != "/api/v1/suites/s1" || got.body != `{"reason":"cleanup"}` || got.header.Get("Idempotency-Key") != "key-1" {
		t.Errorf("write call = %+v, want DELETE /api/v1/suites/s1 with the body and Idempotency-Key key-1", got)
	}
}

func TestGateDoesNotResendUnlessApproved(t *testing.T) {
	cases := []struct {
		name      string
		approvals []reply
		wait      time.Duration
		code      contract.Code
		exit      int
	}{
		{"denied", []reply{decided("denied", "2026-11-14T11:00:00Z", "no")}, time.Hour, contract.ApprovalDenied, 6},
		{"expired", []reply{ok("pending"), ok("expired")}, time.Hour, contract.ApprovalExpired, 7},
		{"wait ran out", []reply{ok("pending")}, 5 * time.Second, contract.PendingApproval, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newGateServer(t, []reply{{202, pendingA1, ""}, {200, deleted, ""}}, tc.approvals...)
			gate := srv.gate(io.Discard, io.Discard)
			gate.Wait = tc.wait

			err := gate.Do(context.Background(), srv.sendDelete("key-1"), sentence)

			code, exit := codeOf(t, err)
			if code != tc.code || exit != tc.exit || len(srv.writeCalls()) != 1 {
				t.Errorf("code %s exit %d with %d writes, want %s exit %d with 1", code, exit, len(srv.writeCalls()), tc.code, tc.exit)
			}
		})
	}
}

func TestGateSecondPendingApprovalExitsSevenWithTheNewApprovalAndNeverSendsThrice(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}, {202, pendingA2, ""}, {200, deleted, ""}}, ok("approved"))

	err := srv.gate(io.Discard, io.Discard).Do(context.Background(), srv.sendDelete("key-1"), sentence)

	var line bytes.Buffer
	var fielded contract.Fielded
	if !errors.As(err, &fielded) {
		t.Fatalf("err = %v, want an error with fields", err)
	}
	_ = json.NewEncoder(&line).Encode(fielded.ErrorFields()["approval"])
	code, exit := codeOf(t, err)
	if code != contract.PendingApproval || exit != 7 || len(srv.writeCalls()) != 2 || !strings.Contains(line.String(), `"id":"a2"`) {
		t.Errorf("code %s exit %d, %d writes, approval %s; want pending_approval exit 7, 2 writes, approval a2", code, exit, len(srv.writeCalls()), line.String())
	}
}

func TestGateDryRunWithWaitReturnsThePendingAtOnceWithoutPolling(t *testing.T) {
	srv := newGateServer(t, []reply{{200, pendingDry, ""}}, ok("approved"))
	var out bytes.Buffer

	err := srv.gate(&out, io.Discard).Do(context.Background(), srv.sendDelete("key-1"), sentence)

	var pending *api.PendingApproval
	_, exit := codeOf(t, err)
	if !errors.As(err, &pending) || !pending.DryRun || exit != 7 || srv.pollCount() != 0 || out.Len() != 0 {
		t.Errorf("err = %v, polls %d, output %q; want the dry-run pending (exit 7), no polls, no output", err, srv.pollCount(), out.String())
	}
}

func TestGateWithoutWaitReturnsThePendingWithoutPolling(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}}, ok("approved"))
	gate := srv.gate(io.Discard, io.Discard)
	gate.Wait = 0

	err := gate.Do(context.Background(), srv.sendDelete("key-1"), sentence)

	var pending *api.PendingApproval
	if !errors.As(err, &pending) || srv.pollCount() != 0 || len(srv.writeCalls()) != 1 {
		t.Errorf("err = %v, polls %d, writes %d; want the pending, no polls, one write", err, srv.pollCount(), len(srv.writeCalls()))
	}
}

func TestGateUserTokenSuccessUnderWaitPrintsTheWriteObjectThenDoneWithoutPolling(t *testing.T) {
	srv := newGateServer(t, []reply{{200, deleted, ""}}, ok("approved"))
	var out bytes.Buffer

	err := srv.gate(&out, io.Discard).Do(context.Background(), srv.sendDelete("key-1"), sentence)

	want := `{"v":1,"suite":{"id":"s1"},"result":"deleted","dry_run":false}` + "\n" +
		`{"v":1,"event":"done","result":"deleted","suite":{"id":"s1"}}` + "\n"
	if err != nil || out.String() != want || srv.pollCount() != 0 || len(srv.writeCalls()) != 1 {
		t.Errorf("err = %v, output:\n%s\npolls %d, writes %d; want:\n%s", err, out.String(), srv.pollCount(), len(srv.writeCalls()), want)
	}
}

func TestGateSuccessWithoutWaitPrintsTheWriteObjectOnly(t *testing.T) {
	srv := newGateServer(t, []reply{{200, deleted, ""}})
	var out bytes.Buffer
	gate := srv.gate(&out, io.Discard)
	gate.Wait = 0

	err := gate.Do(context.Background(), srv.sendDelete("key-1"), sentence)

	if want := `{"v":1,"suite":{"id":"s1"},"result":"deleted","dry_run":false}` + "\n"; err != nil || out.String() != want {
		t.Errorf("err = %v, output %q, want %q", err, out.String(), want)
	}
}

func TestGateCancelledAfterTheApprovedPollDoesNotResend(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}, {200, deleted, ""}}, ok("approved"))
	ctx, cancel := context.WithCancel(context.Background())
	gate := srv.gate(io.Discard, io.Discard)
	gate.Client.HTTP = &http.Client{Transport: cancelAfterBody{http.DefaultTransport, "/api/v1/approvals/", cancel}}

	sends := 0
	send := srv.sendDelete("key-1")

	err := gate.Do(ctx, func(ctx context.Context) (*Outcome, error) {
		sends++
		return send(ctx)
	}, sentence)

	if !errors.Is(err, context.Canceled) || srv.pollCount() != 1 || sends != 1 || len(srv.writeCalls()) != 1 {
		t.Errorf("err = %v, polls %d, sends %d, writes %d; want context.Canceled after one poll and one send", err, srv.pollCount(), sends, len(srv.writeCalls()))
	}
}

type cancelAfterBody struct {
	http.RoundTripper
	prefix string
	cancel context.CancelFunc
}

func (c cancelAfterBody) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.RoundTripper.RoundTrip(r)
	if err == nil && strings.HasPrefix(r.URL.Path, c.prefix) {
		resp.Body = closeHook{resp.Body, c.cancel}
	}
	return resp, err
}

type closeHook struct {
	io.ReadCloser
	onClose func()
}

func (c closeHook) Close() error {
	err := c.ReadCloser.Close()
	c.onClose()
	return err
}

func TestGateTimeoutExitsEightWithoutResending(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}}, ok("pending"))
	gate := srv.gate(io.Discard, io.Discard)
	gate.Timeout = time.Nanosecond

	err := gate.Do(context.Background(), srv.sendDelete("key-1"), sentence)

	code, exit := codeOf(t, err)
	if code != contract.Timeout || exit != 8 || len(srv.writeCalls()) > 1 {
		t.Errorf("code %s exit %d, %d writes; want timeout exit 8 and at most one write", code, exit, len(srv.writeCalls()))
	}
}

func TestGateTextModeWaitsOnStderrAndPrintsTheSentenceOnStdout(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}, {200, deleted, ""}}, ok("pending"), ok("approved"))
	var out, errOut bytes.Buffer
	gate := srv.gate(&out, &errOut)
	gate.JSON = false

	err := gate.Do(context.Background(), srv.sendDelete("key-1"), sentence)

	wantErr := "Approval needed: https://selective.example/a1. Waiting for a person to decide it.\nwaiting for approval (0s)\n"
	if err != nil || out.String() != "Suite deleted\n" || errOut.String() != wantErr {
		t.Errorf("err = %v, stdout %q, stderr %q; want %q and %q", err, out.String(), errOut.String(), "Suite deleted\n", wantErr)
	}
}

func TestGateRecognisesTheApprovalTheAPIClientDetects(t *testing.T) {
	srv := newGateServer(t, []reply{{202, pendingA1, ""}, {200, `{"token":{"id":"t1"},"result":"revoked","dry_run":false}`, ""}}, ok("approved"))
	client := api.New(srv.URL, "tok")
	send := func(ctx context.Context) (*Outcome, error) {
		revoked, err := client.WithContext(ctx).RevokeToken("t1", false)
		if err != nil {
			return nil, err
		}
		return &Outcome{Key: "token", Resource: revoked.Token, Result: revoked.Result}, nil
	}
	var out bytes.Buffer

	err := srv.gate(&out, io.Discard).Do(context.Background(), send, sentence)

	if err != nil || len(srv.writeCalls()) != 2 || !strings.HasSuffix(out.String(), `{"v":1,"event":"done","result":"revoked","token":{"id":"t1"}}`+"\n") {
		t.Errorf("err = %v, writes %d, output:\n%s", err, len(srv.writeCalls()), out.String())
	}
}

func TestBindWaitAddsTheFlagsAndAnnotationsTheSchemaAccepts(t *testing.T) {
	var gate Gate
	root := &cobra.Command{Use: "sel"}
	cmd := &cobra.Command{Use: "drop", Short: "Drop it", RunE: func(*cobra.Command, []string) error { return nil }}
	BindWait(cmd, &gate)
	root.AddCommand(cmd)

	doc, err := schema.Build(root, "test")
	node, found := schema.Lookup(doc, []string{"drop"})

	if err != nil || !found || node.WaitDefault != "10m" || strings.Join(node.Events, ",") != "waiting,done" {
		t.Fatalf("Build err = %v, found %v, node %+v; want wait_default 10m and events waiting,done", err, found, node)
	}
	for args, want := range map[string][2]time.Duration{"--wait": {10 * time.Minute, 0}, "--wait=5s --timeout=1m": {5 * time.Second, time.Minute}, "": {0, 0}} {
		gate = Gate{}
		cmd.ResetFlags()
		BindWait(cmd, &gate)
		if err := cmd.ParseFlags(strings.Fields(args)); err != nil || gate.Wait != want[0] || gate.Timeout != want[1] {
			t.Errorf("%q parsed to wait %s timeout %s (%v), want %s and %s", args, gate.Wait, gate.Timeout, err, want[0], want[1])
		}
	}
	if usages := cmd.Flags().FlagUsages(); strings.Count(usages, "(default 0s)") != 0 {
		t.Errorf("flag usages show a zero default:\n%s", usages)
	}
}

func TestGateRefusesNonPositiveWaitAndTimeoutBeforeSending(t *testing.T) {
	for _, arg := range []string{"--wait=0s", "--timeout=-1s"} {
		t.Run(arg, func(t *testing.T) {
			var gate Gate
			cmd := &cobra.Command{Use: "drop"}
			BindWait(cmd, &gate)
			if err := cmd.ParseFlags([]string{arg}); err != nil {
				t.Fatal(err)
			}
			sends := 0

			err := gate.Do(context.Background(), func(context.Context) (*Outcome, error) {
				sends++
				return nil, nil
			}, sentence)

			code, exit := codeOf(t, err)
			if code != contract.UsageError || exit != 5 || sends != 0 {
				t.Errorf("code %s exit %d, %d sends; want usage_error exit 5 and none", code, exit, sends)
			}
		})
	}
}
