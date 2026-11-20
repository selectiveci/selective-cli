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

	"github.com/mattn/go-isatty"
	"github.com/selectiveci/selective-cli/internal/contract"
)

const Version = 1

type field struct {
	key   string
	value any
}

var (
	isTTY       = isatty.IsTerminal
	isCygwinTTY = isatty.IsCygwinTerminal
)

func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
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
	if f, ok := err.(contract.Fielded); ok {
		extras := maps.Clone(f.ErrorFields())
		for _, reserved := range []string{"v", "error", "message"} {
			delete(extras, reserved)
		}
		fields = append(fields, sorted(extras)...)
	}
	return line(w, fields...)
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
