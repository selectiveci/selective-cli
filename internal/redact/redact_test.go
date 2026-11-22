package redact_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/redact"
)

func TestCodedCleansMessageAndNestedFieldsKeepingCodeAndExit(t *testing.T) {
	t.Cleanup(redact.Reset)
	secret := `sk_live_q"u\o<te`
	redact.Add("")
	redact.Add("sk_live_q")
	redact.Add(secret)
	original := &api.Error{Status: http.StatusForbidden, Code: "unregistered", Message: "bad " + secret, Fields: map[string]any{
		"nested": map[string]any{"list": []any{"x " + secret}},
		"count":  json.Number("12345678901234567890"),
	}}

	got := redact.Coded(original)

	raw, _ := json.Marshal(got.(contract.Fielded).ErrorFields())
	want := `{"count":12345678901234567890,"nested":{"list":["x [redacted]"]}}`
	if got.Error() != "bad [redacted]" || string(raw) != want || got.ErrorCode() != "unregistered" || got.ExitCode() != contract.ExitForbidden {
		t.Errorf("message %q fields %s code %s exit %d", got.Error(), raw, got.ErrorCode(), got.ExitCode())
	}
}
