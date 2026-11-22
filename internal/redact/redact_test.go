package redact_test

import (
	"encoding/json"
	"net/http"
	"strings"
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

func TestStringMatchesShortTokensOnlyAsWholeWordsAndLongTokensAnywhere(t *testing.T) {
	fifteen, sixteen, forty := strings.Repeat("k", 15), strings.Repeat("k", 16), "sk_live_"+strings.Repeat("Z", 32)
	cases := []struct {
		name, in, want string
		secrets        []string
	}{
		{"short inside a word", "Invalid token.", "Invalid token.", []string{"tok"}},
		{"several short tokens leave ordinary words alone", "Invalid token.", "Invalid token.", []string{"tok", "to", "e", "a"}},
		{"short standing alone", "bad credential tok here", "bad credential [redacted] here", []string{"tok"}},
		{"short after an equals sign", "token=tok", "token=[redacted]", []string{"tok"}},
		{"short filling the string", "tok", "[redacted]", []string{"tok"}},
		{"short beside an underscore", "a_tok b", "a_tok b", []string{"tok"}},
		{"short beside a hyphen", "tok-x", "tok-x", []string{"tok"}},
		{"fifteen characters need word edges", "x" + fifteen + "y", "x" + fifteen + "y", []string{fifteen}},
		{"sixteen characters match anywhere", "x" + sixteen + "y", "x[redacted]y", []string{sixteen}},
		{"forty characters embedded", "x%3D" + forty + "y", "x%3D[redacted]y", []string{forty}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(redact.Reset)
			for _, secret := range tc.secrets {
				redact.Add(secret)
			}

			if got := redact.String(tc.in); got != tc.want {
				t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCodedKeepsKeysNumbersAndStructureWhileCleaningShortTokenStrings(t *testing.T) {
	t.Cleanup(redact.Reset)
	redact.Add("tok")
	kept := &struct{ N int }{1}
	original := &api.Error{Status: http.StatusUnprocessableEntity, Code: "x", Message: "m", Fields: map[string]any{
		"tok":    true,
		"detail": map[string]any{"token": nil, "count": json.Number("12345678901234567890"), "list": []any{"tok", "token", false}},
		"kept":   kept,
	}}

	got := redact.Coded(original).(contract.Fielded).ErrorFields()

	raw, _ := json.Marshal(got)
	want := `{"[redacted]":true,"detail":{"count":12345678901234567890,"list":["[redacted]","token",false],"token":null},"kept":{"N":1}}`
	if string(raw) != want || got["kept"] != kept {
		t.Errorf("fields %s, want %s", raw, want)
	}
}

func TestCodedCleansTheErrorCodeAndKeepsTheOriginalExit(t *testing.T) {
	t.Cleanup(redact.Reset)
	secret := "sk_live_" + strings.Repeat("c", 24)
	redact.Add(secret)
	original := &api.Error{Status: http.StatusUnprocessableEntity, Code: contract.Code(secret), Message: "m"}

	got := redact.Coded(original)

	if got.ErrorCode() != "[redacted]" || got.ExitCode() != original.ExitCode() || got.ExitCode() != contract.ExitUsage {
		t.Errorf("code %q exit %d, want [redacted] and exit %d", got.ErrorCode(), got.ExitCode(), contract.ExitUsage)
	}
}
