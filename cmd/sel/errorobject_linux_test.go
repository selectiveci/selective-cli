package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func oneObjectUnder512(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(stdout), &object); err != nil || len(stdout) > 512 || strings.Count(stdout, "\n") != 1 || object["v"] != float64(1) {
		t.Fatalf("stdout is %d bytes, not one v1 JSON object of at most 512: %v: %.80q", len(stdout), err, stdout)
	}
	return object
}

func TestTimeoutKeepsEveryJSONErrorObjectUnder512BytesWhateverTheServerSends(t *testing.T) {
	cases := map[string]struct {
		message string
		body    map[string]any
	}{
		"long ascii":            {strings.Repeat("m", 4000), nil},
		"four-byte characters":  {strings.Repeat("😀", 400), nil},
		"escapes that expand":   {strings.Repeat("\"\x01\u2028", 300), nil},
		"oversized extra field": {"short", map[string]any{"big": strings.Repeat("b", 3000), "hint": "kept"}},
		"oversized error code":  {"", map[string]any{"error": strings.Repeat("c", 2000)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"error": "validation_failed", "message": c.message}
			maps.Copy(body, c.body)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(body)
			}))
			t.Cleanup(srv.Close)
			res, _ := runCapped(t, 5*time.Second, t.TempDir(), t.TempDir(), srv.URL, nil, "approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90", "--json", "--timeout=5s")

			object := oneObjectUnder512(t, res.stdout)
			message, _ := object["message"].(string)
			_, big := object["big"]
			if !utf8.ValidString(message) || !strings.HasPrefix(c.message, message) || big || (c.body["hint"] != nil && object["hint"] != "kept") {
				t.Errorf("object %.200q; want a clean prefix of the message, the oversized field dropped whole and the small one kept", res.stdout)
			}
		})
	}
}

func TestTimeoutMovesDoctorsDocumentToStderrAndLeavesNoTimeoutUntouched(t *testing.T) {
	host := "http://127.0.0.1:1/" + strings.Repeat("h", 3000)
	capped, _ := runCapped(t, 5*time.Second, checkoutOfAcmeWeb(t), t.TempDir(), host, nil, "doctor", "--json", "--timeout=5s")
	plain, _ := runCapped(t, 15*time.Second, checkoutOfAcmeWeb(t), t.TempDir(), host, nil, "doctor", "--json")

	object := oneObjectUnder512(t, capped.stdout)
	doctorLine, rest, _ := strings.Cut(capped.stderr, "\n")
	if !json.Valid([]byte(doctorLine)) || !strings.HasPrefix(doctorLine, `{"v":1,"doctor":{`) || len(doctorLine) <= 512 || !strings.HasPrefix(rest, "Error: ") {
		t.Errorf("stderr %d bytes %.100q; want doctor's whole document as one line, then the Error: line", len(doctorLine), capped.stderr)
	}
	if _, has := object["doctor"]; has || object["failed_check"] != "server" || capped.exit != plain.exit {
		t.Errorf("object %v, exit %d against %d; want failed_check server in place of doctor and the same exit", object, capped.exit, plain.exit)
	}
	if len(plain.stdout) <= 512 || !strings.Contains(plain.stdout, `"doctor":{`) || strings.Contains(plain.stderr, `{"v":1`) {
		t.Errorf("without --timeout stdout is %d bytes and stderr %.80q; want doctor's document on stdout as in v0.4.0", len(plain.stdout), plain.stderr)
	}
}
