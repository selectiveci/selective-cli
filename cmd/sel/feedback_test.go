package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/selectiveci/selective-cli/internal/schema"
)

type feedbackCall struct {
	method, path, query, key, contentType, body string
}

const feedbackCreated = `{"feedback":{"id":"f1"},"result":"created","dry_run":false}`

func feedbackOnce(t *testing.T, replies []stubReply, stdin string, args ...string) (cliResult, []feedbackCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []feedbackCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, feedbackCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type"), string(body)})
		reply := replies[min(len(calls), len(replies))-1]
		if reply.retryAfter != "" {
			w.Header().Set("Retry-After", reply.retryAfter)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(srv.Close)
	res := runBinaryStdin(t, t.TempDir(), srv.URL, t.TempDir(), stdin, append([]string{"feedback"}, args...)...)
	mu.Lock()
	defer mu.Unlock()
	return res, calls
}

func TestFeedbackPostsTheJSONBodyToTheOrgAndRelaysTheWriteObject(t *testing.T) {
	res, calls := feedbackOnce(t, []stubReply{{status: 201, body: feedbackCreated}}, "", "needs a dark mode", "--org", "acme", "--json")

	if len(calls) != 1 || calls[0].key == "" {
		t.Fatalf("requests = %+v, want one carrying an Idempotency-Key", calls)
	}
	want := feedbackCall{"POST", "/api/v1/orgs/acme/feedback", "", calls[0].key, "application/json", `{"text":"needs a dark mode","source":"cli","client_version":"dev"}`}
	if calls[0] != want || res.exit != 0 || res.stdout != `{"v":1,`+strings.TrimPrefix(feedbackCreated, "{")+"\n" {
		t.Errorf("request %+v, want %+v; exit %d stdout %q", calls[0], want, res.exit, res.stdout)
	}
}

func TestFeedbackTakesTheTextFromStdinOnlyWithoutAnArgument(t *testing.T) {
	reply := []stubReply{{status: 201, body: feedbackCreated}}
	_, viaArg := feedbackOnce(t, reply, "", "hello there", "--org", "acme")
	_, viaStdin := feedbackOnce(t, reply, "hello there\n", "--org", "acme")
	_, both := feedbackOnce(t, reply, "from stdin", "hello there", "--org", "acme")

	if len(viaArg) != 1 || len(viaStdin) != 1 || len(both) != 1 || viaStdin[0].body != viaArg[0].body || both[0].body != viaArg[0].body {
		t.Errorf("argument sent %+v, stdin sent %+v, both sent %+v, want one identical body each", viaArg, viaStdin, both)
	}
}

func TestFeedbackRefusesOrDryRunsWithoutARequestOrAnOrg(t *testing.T) {
	cases := map[string]struct {
		stdin string
		args  []string
		exit  int
		out   string
	}{
		"empty argument":   {"", []string{""}, 5, `"error":"usage_error"`},
		"blank stdin":      {" \n\t\n", nil, 5, `"error":"usage_error"`},
		"over the cap":     {strings.Repeat("a", 8193), nil, 5, `"error":"usage_error"`},
		"dry run too long": {"", []string{strings.Repeat("é", 4097), "--dry-run"}, 5, `"error":"usage_error"`},
		"dry run":          {"", []string{"nice tool", "--dry-run"}, 0, `{"v":1,"feedback":{"text":"nice tool","source":"cli","client_version":"dev"},"result":"created","dry_run":true}` + "\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, calls := feedbackOnce(t, []stubReply{{status: 201, body: feedbackCreated}}, tc.stdin, append(tc.args, "--json")...)

			if res.exit != tc.exit || len(calls) != 0 || !strings.Contains(res.stdout, tc.out) {
				t.Errorf("exit %d with %d requests, stdout %q, want exit %d, none and %q", res.exit, len(calls), res.stdout, tc.exit, tc.out)
			}
		})
	}
}

func TestFeedbackRetriesATransientFailureWithTheSameKey(t *testing.T) {
	res, calls := feedbackOnce(t, []stubReply{
		{status: 503, retryAfter: "0", body: `{"error":"server_error","message":"Try again."}`},
		{status: 201, body: feedbackCreated},
	}, "", "retry me", "--org", "acme", "--json")

	if len(calls) != 2 || calls[0].key == "" || calls[0].key != calls[1].key || res.exit != 0 {
		t.Errorf("exit %d requests = %+v, want two carrying the same non-empty key", res.exit, calls)
	}
}

func TestFeedbackIdempotencyKeyFlagSendsThatKey(t *testing.T) {
	_, calls := feedbackOnce(t, []stubReply{{status: 201, body: feedbackCreated}}, "", "hi", "--org", "acme", "--idempotency-key", "agent-key-1", "--json")

	if len(calls) != 1 || calls[0].key != "agent-key-1" {
		t.Errorf("requests = %+v, want one with key agent-key-1", calls)
	}
}

func TestFeedbackRefusesAnEmptyIdempotencyKeyBeforeSending(t *testing.T) {
	res, calls := feedbackOnce(t, []stubReply{{status: 201, body: feedbackCreated}}, "", "hi", "--org", "acme", "--idempotency-key", "", "--json")

	if res.exit != 5 || !strings.Contains(res.stdout, `"error":"usage_error"`) || len(calls) != 0 {
		t.Errorf("exit %d stdout %q with %d requests, want exit 5 usage_error and none", res.exit, res.stdout, len(calls))
	}
}

func TestFeedbackMapsServerAnswersToExitCodesAndALastLineErrorObject(t *testing.T) {
	cases := []struct {
		status, exit int
		retryAfter   string
		body, last   string
	}{
		{429, 4, "3600", `{"error":"rate_limited","message":"Too much.","retry_after_s":3600}`, `"error":"rate_limited"`},
		{422, 5, "", `{"error":"invalid_request","message":"text must not contain NUL bytes"}`, `"error":"invalid_request"`},
		{401, 4, "", `{"error":"unauthorized","message":"Bad token."}`, `"error":"unauthorized"`},
		{403, 6, "", `{"error":"forbidden","message":"No."}`, `"error":"forbidden"`},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			res, calls := feedbackOnce(t, []stubReply{{status: tc.status, retryAfter: tc.retryAfter, body: tc.body}}, "", "hi", "--org", "acme", "--json")

			lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
			last := lines[len(lines)-1]
			if res.exit != tc.exit || !strings.Contains(last, tc.last) || len(calls) != 1 || tc.retryAfter != "" && !strings.Contains(last, `"retry_after_s":3600`) {
				t.Errorf("exit %d, last stdout line %q, %d requests, want exit %d with %s once", res.exit, last, len(calls), tc.exit, tc.last)
			}
		})
	}
}

func TestHelpDeclaresFeedbackAsANonDestructiveWriteWithOptionalTextAndDryRun(t *testing.T) {
	for _, c := range helpCommands(t) {
		if c.Path != "feedback" {
			continue
		}
		hasDryRun := slices.ContainsFunc(c.Flags, func(f schema.Flag) bool { return f.Name == "dry-run" })
		if !c.Mutates || c.Destructive || len(c.Args) != 1 || c.Args[0].Name != "TEXT" || c.Args[0].Required || !hasDryRun {
			t.Errorf("feedback = %+v, want a non-destructive write with an optional TEXT and --dry-run", c)
		}
		return
	}
	t.Error("help --json has no feedback command")
}
