package redact

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const (
	Mask           = "[redacted]"
	anywhereLength = 16
	wordBytes      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
)

var (
	mu      sync.Mutex
	secrets []string
)

func Add(secret string) {
	mu.Lock()
	defer mu.Unlock()
	if secret == "" || slices.Contains(secrets, secret) {
		return
	}
	secrets = append(secrets, secret)
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
}

func Reset() {
	mu.Lock()
	defer mu.Unlock()
	secrets = nil
}

func known() []string {
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(secrets)
}

func String(s string) string {
	list := known()
	var out strings.Builder
	copied := 0
	for i := 0; i < len(s); {
		n := matchAt(s, i, list)
		if n == 0 {
			i++
			continue
		}
		out.WriteString(s[copied:i])
		out.WriteString(Mask)
		i += n
		copied = i
	}
	if copied == 0 {
		return s
	}
	out.WriteString(s[copied:])
	return out.String()
}

func matchAt(s string, i int, list []string) int {
	for _, secret := range list {
		if !strings.HasPrefix(s[i:], secret) {
			continue
		}
		if len(secret) >= anywhereLength || standsAlone(s, i, i+len(secret)) {
			return len(secret)
		}
	}
	return 0
}

func standsAlone(s string, start, end int) bool {
	return (start == 0 || !wordByte(s[start-1])) && (end == len(s) || !wordByte(s[end]))
}

func wordByte(b byte) bool {
	return strings.IndexByte(wordBytes, b) >= 0
}

func walk(v any) (any, bool) {
	switch node := v.(type) {
	case string:
		clean := String(node)
		return clean, clean != node
	case []any:
		changed := false
		for i, item := range node {
			var cut bool
			node[i], cut = walk(item)
			changed = changed || cut
		}
		return node, changed
	case map[string]any:
		out := make(map[string]any, len(node))
		changed := false
		for key, item := range node {
			clean, cut := walk(item)
			cleanKey := String(key)
			out[cleanKey] = clean
			changed = changed || cut || cleanKey != key
		}
		return out, changed
	}
	return v, false
}

func value(v any) (any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return v, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree any
	if decoder.Decode(&tree) != nil {
		return v, false
	}
	clean, changed := walk(tree)
	if !changed {
		return v, false
	}
	return clean, true
}

func Raw(raw json.RawMessage) json.RawMessage {
	clean, changed := value(raw)
	if !changed {
		return raw
	}
	out, _ := json.Marshal(clean)
	return out
}

type cleaned struct {
	contract.Coded
	code    contract.Code
	message string
	fields  map[string]any
}

func (c cleaned) Error() string               { return c.message }
func (c cleaned) ErrorCode() contract.Code    { return c.code }
func (c cleaned) ErrorFields() map[string]any { return c.fields }

func Coded(err contract.Coded) contract.Coded {
	message := String(err.Error())
	code := contract.Code(String(string(err.ErrorCode())))
	changed := message != err.Error() || code != err.ErrorCode()
	var fields map[string]any
	if fielded, ok := err.(contract.Fielded); ok {
		fields = make(map[string]any)
		for key, v := range fielded.ErrorFields() {
			cleanValue, cut := value(v)
			cleanKey := String(key)
			fields[cleanKey] = cleanValue
			changed = changed || cut || cleanKey != key
		}
	}
	if !changed {
		return err
	}
	return cleaned{err, code, message, fields}
}
