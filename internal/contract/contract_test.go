package contract

import "testing"

func TestRegistryExitCodes(t *testing.T) {
	want := map[Code]int{
		"unauthorized": 4, "insufficient_scope": 6, "forbidden": 6, "token_scope_exceeded": 6,
		"not_enabled": 6, "approval_denied": 6, "not_found": 3, "organization_not_found": 3,
		"suite_not_found": 3, "no_build": 3, "runner_not_found": 3, "git_repo_not_found": 3, "invalid_request": 5,
		"validation_failed": 5, "name_required": 5, "repo_required": 5, "invalid_type": 5,
		"invalid": 5, "repo_not_installed": 5, "name_taken": 5, "invalid_scope": 5,
		"invalid_key": 5, "idempotency_key_reused": 5, "ambiguous_build": 5,
		"run_not_running": 5, "run_running": 5, "suite_archived": 5, "suite_not_archived": 5,
		"suite_has_runs": 5, "last_owner": 5, "cannot_remove_self": 5,
		"cannot_change_own_role": 5, "request_in_progress": 4, "rate_limited": 4,
		"server_error": 4, "pending_approval": 7, "approval_expired": 7, "network_error": 4,
		"usage_error": 5, "timeout": 8, "internal_error": 9,
		"runner_error": 1, "no_framework": 5, "runner_missing": 5, "interrupted": 130,
	}
	for code, exit := range want {
		if got := ExitFor(code, 0); got != exit {
			t.Errorf("ExitFor(%q) = %d, want %d", code, got, exit)
		}
	}
	if len(Codes()) != len(want) {
		t.Errorf("registry has %d codes, test table has %d; add the new row to both", len(Codes()), len(want))
	}
}

func TestUnknownCodeFallsBackToItsHTTPStatus(t *testing.T) {
	cases := map[int]int{
		400: 5, 401: 4, 403: 6, 404: 3, 406: 5, 409: 5, 422: 5, 429: 4, 500: 4, 502: 4, 0: 9, 418: 9,
	}
	for status, exit := range cases {
		if got := ExitFor("a_code_nobody_registered", status); got != exit {
			t.Errorf("status %d: exit %d, want %d", status, got, exit)
		}
	}
}

func TestDefaultCodesAreTheSevenErrorExitsAndEachCodeExitsWithItsOwnExit(t *testing.T) {
	want := map[int]Code{3: "not_found", 4: "network_error", 5: "usage_error", 6: "forbidden", 8: "timeout", 9: "internal_error", 130: "interrupted"}
	found := 0
	for exit := 0; exit <= 255; exit++ {
		code, ok := DefaultCode(exit)
		if !ok {
			continue
		}
		found++
		if code != want[exit] || ExitFor(code, 0) != exit {
			t.Errorf("DefaultCode(%d) = %q, ExitFor = %d, want %q", exit, code, ExitFor(code, 0), want[exit])
		}
	}
	if found != len(want) {
		t.Errorf("%d exits have a default code, want %d", found, len(want))
	}
}

func TestOnlyExitFourIsRetryable(t *testing.T) {
	for exit := -1; exit <= 255; exit++ {
		if Retryable(exit) != (exit == 4) {
			t.Errorf("Retryable(%d) = %v", exit, Retryable(exit))
		}
	}
}
