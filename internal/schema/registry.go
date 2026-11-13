package schema

import "github.com/selectiveci/selective-cli/internal/contract"

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
	{contract.ExitFailures, "Test failures. Only outcome commands use it.", false},
	{contract.ExitFlaky, "Every failure passed on retry. Outcome commands only.", false},
	{contract.ExitNotFound, "Not found, or not there yet. Includes local setup that is missing.", false},
	{contract.ExitRetryable, "Auth, network or server. Retry, or log in again.", true},
	{contract.ExitUsage, "Usage, invalid input, or a conflict with the current state. Fix the call.", false},
	{contract.ExitForbidden, "Forbidden: this token or role may not do it.", false},
	{contract.ExitPending, "Pending approval: a person must approve.", false},
	{contract.ExitTimeout, "Timeout: the thing exists but did not finish in time.", false},
	{contract.ExitInternal, "Internal: an unclassified CLI failure. Do not retry blindly.", false},
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
}

func knownExit(code int) bool {
	for _, e := range exitCodes {
		if e.Code == code {
			return true
		}
	}
	return false
}

func knownEvent(name string) bool {
	for _, e := range events {
		if e.Name == name {
			return true
		}
	}
	return false
}
