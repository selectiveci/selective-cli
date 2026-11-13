package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/selectiveci/selective-cli/internal/contract"
)

var ErrUnauthorized = errors.New("unauthorized")

type Error struct {
	Status     int
	Code       contract.Code
	Message    string
	Scope      string
	InstallURL string
	Fields     map[string]any
	cause      error
}

func (e *Error) Error() string {
	switch {
	case e.Code == contract.InsufficientScope && e.Scope == "setup":
		return fmt.Sprintf("this login can't do that (it needs the %q scope); run: sel auth login --setup", e.Scope)
	case e.Message != "":
		return e.Message
	case e.Code != "":
		return fmt.Sprintf("server refused: %s (HTTP %d)", e.Code, e.Status)
	default:
		return fmt.Sprintf("server returned %d", e.Status)
	}
}

func (e *Error) ErrorCode() contract.Code    { return e.Code }
func (e *Error) ExitCode() int               { return contract.ExitFor(e.Code, e.Status) }
func (e *Error) ErrorFields() map[string]any { return e.Fields }
func (e *Error) Unwrap() error               { return e.cause }

func (e *Error) Is(target error) bool {
	return target == ErrUnauthorized && e.Status == http.StatusUnauthorized
}

func networkError(host string, err error) *Error {
	return &Error{
		Code:    contract.NetworkError,
		Message: fmt.Sprintf("could not reach %s: %v", host, err),
		cause:   err,
	}
}

func decodeError(resp *http.Response) *Error {
	e := &Error{Status: resp.StatusCode}
	var body map[string]any
	if json.NewDecoder(resp.Body).Decode(&body) == nil {
		if code, ok := body["error"].(string); ok {
			e.Code = contract.Code(code)
		}
		e.Message, _ = body["message"].(string)
		e.Scope, _ = body["scope"].(string)
		e.InstallURL, _ = body["install_url"].(string)
		delete(body, "error")
		delete(body, "message")
		e.Fields = body
	}
	if e.Fields == nil {
		e.Fields = map[string]any{}
	}
	if retry, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		e.Fields["retry_after_s"] = retry
	}
	if e.Code == "" {
		e.Code = codeForStatus(resp.StatusCode)
	}
	return e
}

func codeForStatus(status int) contract.Code {
	switch {
	case status == http.StatusUnauthorized:
		return contract.Unauthorized
	case status == http.StatusForbidden:
		return contract.Forbidden
	case status == http.StatusNotFound:
		return contract.NotFound
	case status == http.StatusTooManyRequests:
		return contract.RateLimited
	case status >= 500:
		return contract.ServerError
	default:
		return contract.InvalidRequest
	}
}
