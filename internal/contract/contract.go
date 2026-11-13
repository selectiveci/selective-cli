package contract

import "fmt"

type Code string

const (
	Unauthorized         Code = "unauthorized"
	InsufficientScope    Code = "insufficient_scope"
	Forbidden            Code = "forbidden"
	TokenScopeExceeded   Code = "token_scope_exceeded"
	NotEnabled           Code = "not_enabled"
	ApprovalDenied       Code = "approval_denied"
	NotFound             Code = "not_found"
	OrganizationNotFound Code = "organization_not_found"
	SuiteNotFound        Code = "suite_not_found"
	NoBuild              Code = "no_build"
	RunnerNotFound       Code = "runner_not_found"
	InvalidRequest       Code = "invalid_request"
	ValidationFailed     Code = "validation_failed"
	NameRequired         Code = "name_required"
	RepoRequired         Code = "repo_required"
	InvalidType          Code = "invalid_type"
	Invalid              Code = "invalid"
	RepoNotInstalled     Code = "repo_not_installed"
	NameTaken            Code = "name_taken"
	InvalidScope         Code = "invalid_scope"
	InvalidKey           Code = "invalid_key"
	IdempotencyKeyReused Code = "idempotency_key_reused"
	AmbiguousBuild       Code = "ambiguous_build"
	RunNotRunning        Code = "run_not_running"
	RunRunning           Code = "run_running"
	SuiteArchived        Code = "suite_archived"
	SuiteNotArchived     Code = "suite_not_archived"
	SuiteHasRuns         Code = "suite_has_runs"
	LastOwner            Code = "last_owner"
	CannotRemoveSelf     Code = "cannot_remove_self"
	CannotChangeOwnRole  Code = "cannot_change_own_role"
	RequestInProgress    Code = "request_in_progress"
	RateLimited          Code = "rate_limited"
	ServerError          Code = "server_error"
	PendingApproval      Code = "pending_approval"
	ApprovalExpired      Code = "approval_expired"
	NetworkError         Code = "network_error"
	UsageError           Code = "usage_error"
	Timeout              Code = "timeout"
	InternalError        Code = "internal_error"
)

const (
	ExitOK          = 0
	ExitFailures    = 1
	ExitFlaky       = 2
	ExitNotFound    = 3
	ExitRetryable   = 4
	ExitUsage       = 5
	ExitForbidden   = 6
	ExitPending     = 7
	ExitTimeout     = 8
	ExitInternal    = 9
	ExitInterrupted = 130
)

var exits = map[Code]int{
	Unauthorized:         ExitRetryable,
	InsufficientScope:    ExitForbidden,
	Forbidden:            ExitForbidden,
	TokenScopeExceeded:   ExitForbidden,
	NotEnabled:           ExitForbidden,
	ApprovalDenied:       ExitForbidden,
	NotFound:             ExitNotFound,
	OrganizationNotFound: ExitNotFound,
	SuiteNotFound:        ExitNotFound,
	NoBuild:              ExitNotFound,
	RunnerNotFound:       ExitNotFound,
	InvalidRequest:       ExitUsage,
	ValidationFailed:     ExitUsage,
	NameRequired:         ExitUsage,
	RepoRequired:         ExitUsage,
	InvalidType:          ExitUsage,
	Invalid:              ExitUsage,
	RepoNotInstalled:     ExitUsage,
	NameTaken:            ExitUsage,
	InvalidScope:         ExitUsage,
	InvalidKey:           ExitUsage,
	IdempotencyKeyReused: ExitUsage,
	AmbiguousBuild:       ExitUsage,
	RunNotRunning:        ExitUsage,
	RunRunning:           ExitUsage,
	SuiteArchived:        ExitUsage,
	SuiteNotArchived:     ExitUsage,
	SuiteHasRuns:         ExitUsage,
	LastOwner:            ExitUsage,
	CannotRemoveSelf:     ExitUsage,
	CannotChangeOwnRole:  ExitUsage,
	RequestInProgress:    ExitRetryable,
	RateLimited:          ExitRetryable,
	ServerError:          ExitRetryable,
	PendingApproval:      ExitPending,
	ApprovalExpired:      ExitPending,
	NetworkError:         ExitRetryable,
	UsageError:           ExitUsage,
	Timeout:              ExitTimeout,
	InternalError:        ExitInternal,
}

func Registered(code Code) bool {
	_, ok := exits[code]
	return ok
}

func Codes() []Code {
	out := make([]Code, 0, len(exits))
	for c := range exits {
		out = append(out, c)
	}
	return out
}

func ExitFor(code Code, status int) int {
	if exit, ok := exits[code]; ok {
		return exit
	}
	switch {
	case status == 401, status == 429, status >= 500:
		return ExitRetryable
	case status == 403:
		return ExitForbidden
	case status == 404:
		return ExitNotFound
	case status == 400, status == 406, status == 409, status == 422:
		return ExitUsage
	}
	return ExitInternal
}

type Coded interface {
	error
	ErrorCode() Code
	ExitCode() int
}

type Fielded interface {
	ErrorFields() map[string]any
}

type Error struct {
	Code    Code
	Message string
	Fields  map[string]any
}

func (e *Error) Error() string               { return e.Message }
func (e *Error) ErrorCode() Code             { return e.Code }
func (e *Error) ExitCode() int               { return ExitFor(e.Code, 0) }
func (e *Error) ErrorFields() map[string]any { return e.Fields }

func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
