package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const (
	approvalBytes = `{"id":"a1","url":"https://selective.test/acme/approvals/a1","status":"pending","expires_at":"2026-11-16T00:00:00Z"}`
	pendingBody   = `{"result":"pending_approval","dry_run":false,"approval":` + approvalBytes + `}`
)

func replying(t *testing.T, status int, body string, hits *atomic.Int32) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok")
}

func TestAPendingResultIsReturnedAsPendingApprovalWhateverTheStatus(t *testing.T) {
	for _, c := range []struct {
		status   int
		body     string
		dryRun   bool
		approval bool
	}{
		{202, pendingBody, false, true},
		{200, `{"result":"pending_approval","dry_run":true}`, true, false},
	} {
		client := replying(t, c.status, c.body, new(atomic.Int32))
		var out map[string]any

		err := client.do("DELETE", "/api/v1/tokens/x", nil, http.StatusOK, &out)

		var pending *PendingApproval
		if !errors.As(err, &pending) || pending.DryRun != c.dryRun || (pending.Approval != nil) != c.approval {
			t.Fatalf("%d: err = %#v, want PendingApproval{DryRun:%v, approval:%v}", c.status, err, c.dryRun, c.approval)
		}
		if c.approval && string(pending.Approval) != approvalBytes {
			t.Errorf("approval = %s, want the server's bytes %s", pending.Approval, approvalBytes)
		}
		if pending.ExitCode() != contract.ExitPending || pending.ErrorCode() != contract.PendingApproval {
			t.Errorf("code %q exit %d, want pending_approval and 7", pending.ErrorCode(), pending.ExitCode())
		}
	}
}

func TestA202WithoutAPendingResultIsAServerError(t *testing.T) {
	client := replying(t, 202, `{}`, new(atomic.Int32))

	err := client.do("POST", "/api/v1/tokens", nil, http.StatusOK, &struct{}{})

	if codeOf(t, err) != contract.ServerError {
		t.Errorf("err = %v, want server_error", err)
	}
}

func TestATruncatedSuccessBodyIsAnErrorEvenWhenTheCallerDecodesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 202 Accepted\r\nContent-Length: 100\r\n\r\n0123456789")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	err := New(srv.URL, "tok").RevokeCurrent()

	if err == nil {
		t.Error("a 202 cut off after 10 of 100 bytes returned nil, want the read error")
	}
}

func TestOrdinary200And201BodiesStillDecode(t *testing.T) {
	for _, status := range []int{200, 201} {
		client := replying(t, status, `{"result":"created","dry_run":false}`, new(atomic.Int32))
		var out struct {
			Result string `json:"result"`
		}

		if err := client.do("POST", "/api/v1/tokens", nil, http.StatusOK, &out); err != nil || out.Result != "created" {
			t.Errorf("%d: err %v result %q, want created", status, err, out.Result)
		}
	}
}

func TestAPendingApprovalIsNeverRetriedEvenWithAnIdempotencyKey(t *testing.T) {
	var hits atomic.Int32
	client := replying(t, 202, pendingBody, &hits)

	err := client.doWith(http.Header{"Idempotency-Key": {"k"}}, "POST", "/api/v1/tokens", nil, http.StatusOK, &struct{}{})

	var pending *PendingApproval
	if !errors.As(err, &pending) || hits.Load() != 1 {
		t.Errorf("err = %v after %d requests, want a PendingApproval after exactly one", err, hits.Load())
	}
}
