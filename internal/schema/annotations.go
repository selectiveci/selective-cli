package schema

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func annotate(node *Command, c *cobra.Command) error {
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf("%q: "+format, append([]any{node.Path}, args...)...))
	}
	for _, raw := range splitList(c.Annotations["exit_codes"]) {
		code, err := strconv.Atoi(raw)
		if err != nil || !knownExit(code) {
			fail("exit_codes has %q, which is not a registered exit code", raw)
			continue
		}
		node.ExitCodes = append(node.ExitCodes, code)
	}
	for _, name := range splitList(c.Annotations["events"]) {
		if !knownEvent(name) {
			fail("events has %q, which is not a registered event", name)
			continue
		}
		node.Events = append(node.Events, name)
	}
	for _, key := range []string{"mutates", "destructive"} {
		if v, ok := c.Annotations[key]; ok && v != "true" {
			fail("%s must be \"true\" or absent, got %q", key, v)
		}
	}
	node.Mutates = c.Annotations["mutates"] == "true"
	node.Destructive = c.Annotations["destructive"] == "true"
	if v, ok := c.Annotations["wait_default"]; ok {
		if _, err := time.ParseDuration(v); err != nil || c.Flags().Lookup("wait") == nil {
			fail("wait_default %q needs a duration and a --wait flag", v)
		} else {
			node.WaitDefault = v
		}
	}
	for _, name := range splitList(c.Annotations["secret_flags"]) {
		if c.Flags().Lookup(name) == nil {
			fail("secret_flags names --%s, which the command does not have", name)
			continue
		}
		node.SecretFlags = append(node.SecretFlags, name)
	}
	if v, ok := c.Annotations["mcp"]; ok && v != "skip" {
		fail("mcp must be \"skip\" or absent, got %q", v)
	}
	return errors.Join(problems...)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func knownExit(code int) bool {
	for _, e := range ExitCodes() {
		if e.Code == code {
			return true
		}
	}
	return false
}

func knownEvent(name string) bool {
	for _, e := range Events() {
		if e.Name == name {
			return true
		}
	}
	return false
}
