package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"sort"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
	"github.com/selectiveci/selective-cli/internal/contract"
)

const Version = 1

const ErrorLineLimit = 512

type field struct {
	key   string
	value any
}

var (
	isTTY       = isatty.IsTerminal
	isCygwinTTY = isatty.IsCygwinTerminal
)

func Unwrapped(w io.Writer) io.Writer {
	for {
		inner, ok := w.(interface{ Unwrap() io.Writer })
		if !ok {
			return w
		}
		w = inner.Unwrap()
	}
}

func IsTerminal(w io.Writer) bool {
	f, ok := Unwrapped(w).(*os.File)
	return ok && (isTTY(f.Fd()) || isCygwinTTY(f.Fd()))
}

func JSONMode(flags interface{ GetBool(string) (bool, error) }, stdout io.Writer) bool {
	asked, _ := flags.GetBool("json")
	return asked || !IsTerminal(stdout)
}

func Document(w io.Writer, v any) error {
	body, err := encode(v)
	if err != nil {
		return err
	}
	if len(body) < 2 || body[0] != '{' {
		return fmt.Errorf("a JSON document must be an object, got %s", body)
	}
	var buf bytes.Buffer
	buf.WriteString(`{"v":1`)
	if len(body) > 2 {
		buf.WriteByte(',')
	}
	buf.Write(body[1:])
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}

func Resource(w io.Writer, key string, v any) error {
	return line(w, field{key, v})
}

func List(w io.Writer, key string, items any, nextCursor *string) error {
	if rv := reflect.ValueOf(items); rv.Kind() == reflect.Slice && rv.IsNil() {
		items = []any{}
	}
	return line(w, field{key, items}, field{"next_cursor", nextCursor})
}

func Write(w io.Writer, key string, v any, result string, dryRun bool) error {
	return line(w, field{key, v}, field{"result", result}, field{"dry_run", dryRun})
}

func EventLine(w io.Writer, name string, fields map[string]any) error {
	return line(w, append([]field{{"event", name}}, sorted(fields)...)...)
}

func Error(w io.Writer, err contract.Coded) error {
	fields := []field{{"error", string(err.ErrorCode())}, {"message", err.Error()}}
	return line(w, append(fields, errorExtras(err)...)...)
}

func BoundedError(w io.Writer, err contract.Coded) error {
	const open, between, end = `{"v":1,"error":`, `,"message":`, "}\n"
	const messageFloorRoom = 2 + 128 + 5
	room := ErrorLineLimit - len(open) - len(between) - len(end)
	code := fitString(string(err.ErrorCode()), room-2)
	room -= len(code)
	reserved := min(len(fitString(err.Error(), messageFloorRoom)), room)
	var extras bytes.Buffer
	for _, f := range errorExtras(err) {
		key, _ := encode(f.key)
		val, encodeErr := encode(f.value)
		if encodeErr == nil && reserved+extras.Len()+len(key)+len(val)+2 <= room {
			fmt.Fprintf(&extras, ",%s:%s", key, val)
		}
	}
	var buf bytes.Buffer
	buf.WriteString(open)
	buf.Write(code)
	buf.WriteString(between)
	buf.Write(fitString(err.Error(), room-extras.Len()))
	buf.Write(extras.Bytes())
	buf.WriteString(end)
	_, werr := w.Write(buf.Bytes())
	return werr
}

func errorExtras(err contract.Coded) []field {
	f, ok := err.(contract.Fielded)
	if !ok {
		return nil
	}
	extras := maps.Clone(f.ErrorFields())
	for _, reserved := range []string{"v", "error", "message"} {
		delete(extras, reserved)
	}
	return sorted(extras)
}

func fitString(s string, room int) []byte {
	used, end := 2, 0
	for end < len(s) {
		_, size := utf8.DecodeRuneInString(s[end:])
		piece, _ := encode(s[end : end+size])
		if used+len(piece)-2 > room {
			break
		}
		used += len(piece) - 2
		end += size
	}
	cut, _ := encode(s[:end])
	return cut
}

func sorted(m map[string]any) []field {
	out := make([]field, 0, len(m))
	for k, v := range m {
		out = append(out, field{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func line(w io.Writer, fields ...field) error {
	var buf bytes.Buffer
	buf.WriteString(`{"v":1`)
	for _, f := range fields {
		key, err := encode(f.key)
		if err != nil {
			return err
		}
		val, err := encode(f.value)
		if err != nil {
			return err
		}
		buf.WriteByte(',')
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(val)
	}
	buf.WriteString("}\n")
	_, err := w.Write(buf.Bytes())
	return err
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
