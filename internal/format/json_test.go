package format

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mattn/go-isatty"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/pflag"
)

func capture(t *testing.T, fn func(*bytes.Buffer) error) string {
	t.Helper()
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestDocumentShapes(t *testing.T) {
	next := "c2"
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"resource", capture(t, func(b *bytes.Buffer) error { return Resource(b, "build", map[string]any{"id": "b1"}) }),
			`{"v":1,"build":{"id":"b1"}}` + "\n"},
		{"list with cursor", capture(t, func(b *bytes.Buffer) error { return List(b, "builds", []int{1, 2}, &next) }),
			`{"v":1,"builds":[1,2],"next_cursor":"c2"}` + "\n"},
		{"empty list ends null cursor", capture(t, func(b *bytes.Buffer) error { return List(b, "builds", []int(nil), nil) }),
			`{"v":1,"builds":[],"next_cursor":null}` + "\n"},
		{"write", capture(t, func(b *bytes.Buffer) error { return Write(b, "flake", map[string]any{"id": "f"}, "unchanged", true) }),
			`{"v":1,"flake":{"id":"f"},"result":"unchanged","dry_run":true}` + "\n"},
		{"event", capture(t, func(b *bytes.Buffer) error {
			return EventLine(b, "waiting", map[string]any{"for": "build", "elapsed_s": 4})
		}),
			`{"v":1,"event":"waiting","elapsed_s":4,"for":"build"}` + "\n"},
		{"error with extras", capture(t, func(b *bytes.Buffer) error {
			return Error(b, &contract.Error{Code: contract.RateLimited, Message: "slow down", Fields: map[string]any{"retry_after_s": 7}})
		}), `{"v":1,"error":"rate_limited","message":"slow down","retry_after_s":7}` + "\n"},
		{"document keeps its fields in order", capture(t, func(b *bytes.Buffer) error {
			return Document(b, struct {
				Zed   string `json:"zed"`
				Alpha int    `json:"alpha"`
			}{"z", 1})
		}), `{"v":1,"zed":"z","alpha":1}` + "\n"},
		{"empty document", capture(t, func(b *bytes.Buffer) error { return Document(b, map[string]any{}) }),
			`{"v":1}` + "\n"},
		{"html is not escaped", capture(t, func(b *bytes.Buffer) error { return Resource(b, "x", "a & <b>") }),
			`{"v":1,"x":"a & <b>"}` + "\n"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestJSONModeIsTheFlagOrANonTerminal(t *testing.T) {
	flags := pflag.NewFlagSet("t", pflag.ContinueOnError)
	flags.Bool("json", false, "")

	if !JSONMode(flags, &bytes.Buffer{}) {
		t.Error("a pipe should default to JSON")
	}
	if err := flags.Parse([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	if !JSONMode(flags, &bytes.Buffer{}) {
		t.Error("--json should force JSON")
	}
}

func TestWriteSecretFileIsPrivateAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")

	if err := WriteSecretFile(path, []byte("sk_live_1")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	err = WriteSecretFile(path, []byte("other"))

	coded, ok := err.(contract.Coded)
	if !ok || coded.ErrorCode() != contract.UsageError || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second write error = %v, want a usage_error saying the file exists", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "sk_live_1" {
		t.Errorf("file now holds %q, want the original secret", got)
	}
}

func TestIsTerminalCountsAnMSYSTerminal(t *testing.T) {
	isTTY, isCygwinTTY = func(uintptr) bool { return false }, func(uintptr) bool { return true }
	t.Cleanup(func() { isTTY, isCygwinTTY = isatty.IsTerminal, isatty.IsCygwinTerminal })

	if !IsTerminal(os.Stdout) {
		t.Error("IsTerminal = false for a Cygwin/MSYS terminal, want true")
	}
	if IsTerminal(&bytes.Buffer{}) {
		t.Error("IsTerminal = true for a buffer, want false")
	}
}

func TestErrorDropsExtraFieldsThatReuseReservedKeys(t *testing.T) {
	extras := map[string]any{"v": 2, "error": "other", "message": "other", "retry_after_s": 7}

	got := capture(t, func(b *bytes.Buffer) error {
		return Error(b, &contract.Error{Code: contract.RateLimited, Message: "slow down", Fields: extras})
	})

	want := `{"v":1,"error":"rate_limited","message":"slow down","retry_after_s":7}` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDocumentRefusesAnythingButAnObject(t *testing.T) {
	var buf bytes.Buffer
	if err := Document(&buf, []int{1}); err == nil || buf.Len() != 0 {
		t.Fatalf("err = %v, wrote %q", err, buf.String())
	}
}
