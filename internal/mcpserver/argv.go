package mcpserver

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/selectiveci/selective-cli/internal/schema"
)

const (
	timeoutProperty       = "timeout_s"
	minTimeoutSeconds     = 1
	maxTimeoutSeconds     = 600
	defaultTimeoutSeconds = 60
	maxIntegerDigits      = 20
	maxExponentDigits     = 9
)

type Runner struct {
	exe      string
	commands map[string]schema.Command
	shutdown context.Context
	gate     sync.Mutex
	live     sync.WaitGroup
}

func NewRunner(doc schema.Document, exe string, exposure Exposure) *Runner {
	commands := map[string]schema.Command{}
	for _, c := range doc.Commands {
		if exposure.lists(c) {
			commands[toolName(c.Path)] = c
		}
	}
	return &Runner{exe: exe, commands: commands, shutdown: context.Background()}
}

func (r *Runner) Argv(name string, raw json.RawMessage) ([]string, error) {
	argv, _, err := r.prepare(name, raw)
	return argv, err
}

func (r *Runner) prepare(name string, raw json.RawMessage) ([]string, time.Duration, error) {
	c, ok := r.commands[name]
	if !ok {
		return nil, 0, invalidParams("unknown tool %q", name)
	}
	args, err := decodeArguments(raw)
	if err != nil {
		return nil, 0, err
	}
	timeout, err := takeTimeout(args)
	if err != nil {
		return nil, 0, err
	}
	argv := append([]string{r.exe}, strings.Fields(c.Path)...)
	argv = append(argv, "--json")
	var missing []string
	for _, f := range c.Flags {
		if !exposedFlag(c, f) {
			continue
		}
		value, ok := args[propertyName(f.Name)]
		if !ok {
			if f.Required {
				missing = append(missing, propertyName(f.Name))
			}
			continue
		}
		delete(args, propertyName(f.Name))
		parts, err := flagArgv(f, value)
		if err != nil {
			return nil, 0, err
		}
		argv = append(argv, parts...)
	}
	for _, a := range c.Args {
		key := strings.ToLower(a.Name)
		value, ok := args[key]
		if !ok {
			if a.Required {
				missing = append(missing, key)
			}
			continue
		}
		delete(args, key)
		parts, err := positionalArgv(key, a, value)
		if err != nil {
			return nil, 0, err
		}
		argv = append(argv, parts...)
	}
	if len(args) > 0 {
		return nil, 0, invalidParams("unknown properties for %s: %s", name, strings.Join(sortedKeys(args), ", "))
	}
	if len(missing) > 0 {
		return nil, 0, invalidParams("missing required properties for %s: %s", name, strings.Join(missing, ", "))
	}
	return argv, timeout, nil
}

func decodeArguments(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var args map[string]any
	if err := decoder.Decode(&args); err != nil || args == nil {
		return nil, invalidParams("arguments must be a JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalidParams("arguments must be a single JSON object")
	}
	return args, nil
}

func takeTimeout(args map[string]any) (time.Duration, error) {
	value, ok := args[timeoutProperty]
	if !ok {
		return defaultTimeoutSeconds * time.Second, nil
	}
	delete(args, timeoutProperty)
	seconds, err := integer(timeoutProperty, value)
	if err != nil {
		return 0, err
	}
	if seconds < minTimeoutSeconds || seconds > maxTimeoutSeconds {
		return 0, invalidParams("%s must be between %d and %d", timeoutProperty, minTimeoutSeconds, maxTimeoutSeconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

func flagArgv(f schema.Flag, value any) ([]string, error) {
	key := propertyName(f.Name)
	flag := "--" + f.Name
	switch f.Type {
	case "bool":
		on, ok := value.(bool)
		if !ok {
			return nil, invalidParams("%s must be a boolean", key)
		}
		if on {
			return []string{flag}, nil
		}
		return []string{flag + "=false"}, nil
	case "int":
		n, err := integer(key, value)
		if err != nil {
			return nil, err
		}
		return []string{flag + "=" + strconv.FormatInt(n, 10)}, nil
	case "stringArray", "stringSlice":
		items, err := stringList(key, value)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(items))
		for i, item := range items {
			if f.Type == "stringSlice" && !survivesStringSlice(item) {
				return nil, invalidParams("%s element %q would be split or altered by the command's list parsing", key, item)
			}
			out[i] = flag + "=" + item
		}
		return out, nil
	}
	s, ok := value.(string)
	if !ok {
		return nil, invalidParams("%s must be a string", key)
	}
	return []string{flag + "=" + s}, nil
}

func survivesStringSlice(item string) bool {
	record, err := csv.NewReader(strings.NewReader(item)).Read()
	return err == nil && len(record) == 1 && record[0] == item
}

func positionalArgv(key string, a schema.Arg, value any) ([]string, error) {
	var values []string
	if a.Variadic {
		items, err := stringList(key, value)
		if err != nil {
			return nil, err
		}
		values = items
	} else {
		s, ok := value.(string)
		if !ok {
			return nil, invalidParams("%s must be a string", key)
		}
		values = []string{s}
	}
	for _, v := range values {
		if strings.HasPrefix(v, "-") {
			return nil, invalidParams("%s must not start with a dash", key)
		}
	}
	return values, nil
}

func integer(key string, value any) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, invalidParams("%s must be an integer", key)
	}
	n, ok := exactInteger(number.String())
	if !ok {
		return 0, invalidParams("%s must be an integer in the range %d to %d", key, math.MinInt, math.MaxInt)
	}
	return n, nil
}

func exactInteger(lexeme string) (int64, bool) {
	negative := strings.HasPrefix(lexeme, "-")
	lexeme = strings.TrimPrefix(lexeme, "-")
	mantissa, exponent, _ := strings.Cut(strings.ToLower(lexeme), "e")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return 0, true
	}
	scale := clampedExponent(exponent) - int64(len(fraction))
	trimmed := strings.TrimRight(digits, "0")
	scale += int64(len(digits) - len(trimmed))
	if scale < 0 || int64(len(trimmed))+scale > maxIntegerDigits {
		return 0, false
	}
	text := trimmed + strings.Repeat("0", int(scale))
	if negative {
		text = "-" + text
	}
	n, err := strconv.ParseInt(text, 10, strconv.IntSize)
	return n, err == nil
}

func clampedExponent(exponent string) int64 {
	if exponent == "" {
		return 0
	}
	negative := strings.HasPrefix(exponent, "-")
	digits := strings.TrimLeft(strings.TrimLeft(exponent, "+-"), "0")
	if len(digits) > maxExponentDigits {
		digits = strings.Repeat("9", maxExponentDigits)
	}
	if digits == "" {
		return 0
	}
	n, _ := strconv.ParseInt(digits, 10, 64)
	if negative {
		return -n
	}
	return n
}

func stringList(key string, value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, invalidParams("%s must be an array of strings", key)
	}
	out := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, invalidParams("%s must be an array of strings", key)
		}
		out[i] = s
	}
	return out, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func invalidParams(format string, a ...any) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf(format, a...)}
}
