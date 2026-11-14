package schema

import (
	"slices"
	"sort"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type ExitCode struct {
	Code      int    `json:"code"`
	Meaning   string `json:"meaning"`
	Retryable bool   `json:"retryable"`
}

type ErrorCode struct {
	Code string `json:"code"`
	Exit int    `json:"exit"`
}

type Event struct {
	Name      string `json:"name"`
	EmittedBy string `json:"emitted_by"`
}

var exitCodes = []ExitCode{
	{contract.ExitOK, "Success. For outcome commands, every test passed.", false},
	{contract.ExitFailures, "Test failures, or runner_error: the local runner client died or broke the protocol mid-run. Only outcome commands use it.", false},
	{contract.ExitFlaky, "Every failure passed on retry. Outcome commands only.", false},
	{contract.ExitNotFound, "Not found, or not there yet: not_found, no_build, organization_not_found, suite_not_found, runner_not_found. Includes local setup that is missing.", false},
	{contract.ExitRetryable, "Auth, network or server. unauthorized: log in again. network_error, server_error, rate_limited, request_in_progress: retry.", true},
	{contract.ExitUsage, "Usage, invalid input, or a conflict with the current state. Fix the call: usage_error, invalid_request, validation_failed, name_required, repo_required, invalid_type, invalid, repo_not_installed, name_taken, invalid_scope, invalid_key, stale_key, idempotency_key_reused, ambiguous_build, run_not_running, run_running, suite_archived, suite_not_archived, suite_has_runs, last_owner, cannot_remove_self, cannot_change_own_role.", false},
	{contract.ExitForbidden, "Forbidden: this token or role may not do it (insufficient_scope, forbidden, token_scope_exceeded, not_enabled), or a person denied it (approval_denied).", false},
	{contract.ExitPending, "Pending approval: a person must approve (pending_approval), or the approval expired and must be requested again (approval_expired).", false},
	{contract.ExitTimeout, "Timeout: the thing exists but did not finish in time (timeout).", false},
	{contract.ExitInternal, "Internal: an unclassified CLI failure (internal_error). Do not retry blindly.", false},
	{contract.ExitInterrupted, "Interrupted (SIGINT).", false},
}

var events = []Event{
	{"build_start", "server"},
	{"selection", "server"},
	{"progress", "server"},
	{"summary", "server"},
	{"test_fail", "server"},
	{"replay_truncated", "server"},
	{"waiting", "cli"},
	{"heartbeat", "cli"},
	{"rerun_started", "cli"},
	{"done", "cli"},
	{"test_pass", "cli"},
	{"authorize", "cli"},
	{"logged_in", "cli"},
}

func ExitCodes() []ExitCode { return slices.Clone(exitCodes) }

func Events() []Event { return slices.Clone(events) }

func Errors() []ErrorCode {
	codes := contract.Codes()
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	out := make([]ErrorCode, 0, len(codes))
	for _, c := range codes {
		out = append(out, ErrorCode{string(c), contract.ExitFor(c, 0)})
	}
	return out
}
