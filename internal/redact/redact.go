package redact

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const Mask = "[redacted]"

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
	for _, secret := range known() {
		s = strings.ReplaceAll(s, secret, Mask)
	}
	return s
}

func escaped(text string) string {
	for _, secret := range known() {
		quoted, _ := json.Marshal(secret)
		text = strings.ReplaceAll(text, string(quoted[1:len(quoted)-1]), Mask)
	}
	return text
}

func value(v any) (any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return v, false
	}
	clean := escaped(string(raw))
	if clean == string(raw) {
		return v, false
	}
	decoder := json.NewDecoder(strings.NewReader(clean))
	decoder.UseNumber()
	var out any
	if decoder.Decode(&out) != nil {
		return Mask, true
	}
	return out, true
}

type cleaned struct {
	contract.Coded
	message string
	fields  map[string]any
}

func (c cleaned) Error() string               { return c.message }
func (c cleaned) ErrorFields() map[string]any { return c.fields }

func Coded(err contract.Coded) contract.Coded {
	message := String(err.Error())
	changed := message != err.Error()
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
	return cleaned{err, message, fields}
}
