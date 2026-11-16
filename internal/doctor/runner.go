package doctor

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/selectiveci/selective-cli/internal/localrun"
)

func (s *state) checkRunner() Check {
	root, err := s.opts.Root()
	if err != nil {
		return runnerWarn(err)
	}
	if err := localrun.UnsupportedOS(s.opts.OS); err != nil {
		return runnerWarn(err)
	}
	framework, err := localrun.DetectFramework(root, "")
	if err != nil {
		return runnerWarn(err)
	}
	if _, err := localrun.ClientCommand(root, framework, os.Environ()); err != nil {
		return runnerWarn(err)
	}
	if message := s.suiteDisagreement(root, framework); message != "" {
		return Check{Name: "runner", Status: Warn, Message: message}
	}
	return Check{
		Name:    "runner",
		Status:  OK,
		Message: fmt.Sprintf("%s: the client sel test needs is in place", framework),
		Detail:  map[string]any{"framework": string(framework)},
	}
}

func runnerWarn(err error) Check {
	return Check{Name: "runner", Status: Warn, Code: codeOf(err), Message: err.Error()}
}

func (s *state) suiteDisagreement(root string, framework localrun.Framework) string {
	if len(s.suiteTypes) == 0 || slices.Contains(s.suiteTypes, string(framework)) {
		return ""
	}
	types := strings.Join(s.suiteTypes, ", ")
	runnable := slices.ContainsFunc(s.suiteTypes, func(t string) bool {
		_, err := localrun.DetectFramework(root, t)
		return err == nil
	})
	if !runnable {
		return fmt.Sprintf("the suite type (%s) has no local runner, so sel test cannot run it here", types)
	}
	return fmt.Sprintf("this checkout looks like %s but the suite type is %s", framework, types)
}
