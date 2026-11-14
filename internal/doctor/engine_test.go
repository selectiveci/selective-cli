package doctor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const laterTrailer = " socket=skip runner=skip cli_version=skip"

func stub(name, after string, status Status, code contract.Code) step {
	return step{name, after, func(*state) Check {
		return Check{Name: name, Status: status, Code: code, Message: name + " " + string(status)}
	}}
}

func runSteps(t *testing.T, timeout time.Duration, injected ...step) *Report {
	t.Helper()
	saved := steps
	steps = injected
	t.Cleanup(func() { steps = saved })
	done := make(chan *Report, 1)
	go func() { done <- Run(Options{Timeout: timeout}) }()
	select {
	case report := <-done:
		return report
	case <-time.After(5 * time.Second):
		t.Fatalf("Run was still blocked 5s after a %s timeout: the deadline does not bound a hung step", timeout)
		return nil
	}
}

func TestAFailedStepSkipsEverythingThatDependsOnItAndNamesIt(t *testing.T) {
	report := runSteps(t, time.Minute,
		stub("a", "", OK, ""),
		stub("b", "a", Fail, contract.Unauthorized),
		stub("c", "b", OK, ""),
		stub("d", "c", OK, ""),
		stub("e", "a", OK, ""),
	)

	if got, want := shape(report), "a=ok b=fail c=skip d=skip e=ok"+laterTrailer; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	for _, name := range []string{"c", "d"} {
		if got := check(t, report, name).Message; got != "skipped because b failed" {
			t.Errorf("%s message = %q", name, got)
		}
	}
}

func TestAWarningBlocksNothingAndSetsTheReportStatus(t *testing.T) {
	report := runSteps(t, time.Minute, stub("a", "", Warn, ""), stub("b", "a", OK, ""))

	if got, want := shape(report), "a=warn b=ok"+laterTrailer; got != want || report.Status != "warn" || report.Err() != nil {
		t.Errorf("checks = %s, status %q, err %v", got, report.Status, report.Err())
	}
}

func TestTheFirstFailureInOrderDecidesTheErrorAndCarriesEveryCheck(t *testing.T) {
	report := runSteps(t, time.Minute, stub("a", "", Fail, contract.UsageError), stub("b", "", Fail, contract.NetworkError))

	err := coded(t, report)

	if err.ErrorCode() != contract.UsageError || err.ExitCode() != 5 || report.Status != "fail" {
		t.Errorf("err = %v (%s, exit %d), status %q", err, err.ErrorCode(), err.ExitCode(), report.Status)
	}
	if doc := err.(contract.Fielded).ErrorFields()["doctor"]; doc != report {
		t.Errorf("doctor field = %v, want the report", doc)
	}
}

func TestAStepThatOutlastsTheTimeoutFailsAndTheRestSkip(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hang := step{"b", "a", func(*state) Check { <-release; return Check{} }}

	began := time.Now()
	report := runSteps(t, 200*time.Millisecond, stub("a", "", OK, ""), hang, stub("c", "b", OK, ""), stub("d", "", OK, ""))
	elapsed := time.Since(began)

	if got, want := shape(report), "a=ok b=fail c=skip d=skip"+laterTrailer; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	if b := check(t, report, "b"); b.Code != contract.Timeout || exitOf(report) != 8 || elapsed > 2*time.Second {
		t.Errorf("b = %+v, exit %d, took %s", b, exitOf(report), elapsed)
	}
}

func TestASecretInAStepsTextNeverReachesTheReportOrTheError(t *testing.T) {
	leaky := step{"a", "", func(s *state) Check {
		s.secret = "s3cret-value"
		return Check{Name: "a", Status: Fail, Code: contract.Unauthorized, Message: "server echoed s3cret-value", Fix: "unset s3cret-value"}
	}}

	report := runSteps(t, time.Minute, leaky)

	a := check(t, report, "a")
	if a.Message != "server echoed [redacted]" || a.Fix != "unset [redacted]" || coded(t, report).Error() != a.Message {
		t.Errorf("a = %+v, err %v", a, report.Err())
	}
}

func TestARunWhoseDeadlineHasAlreadyPassedFailsWithTimeoutInsteadOfPassing(t *testing.T) {
	report := runSteps(t, 0, stub("a", "", OK, ""), stub("b", "a", OK, ""))

	if got, want := shape(report), "a=fail b=skip"+laterTrailer; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	if a := check(t, report, "a"); a.Code != contract.Timeout || report.Status != "fail" || exitOf(report) != 8 {
		t.Errorf("a = %+v, status %q, exit %d", a, report.Status, exitOf(report))
	}
}

func TestASecretInDetailNeverReachesTheReportJSONOrTheErrorsDoctorField(t *testing.T) {
	leaky := step{"a", "", func(s *state) Check {
		s.secret = "s3cret-value"
		return Check{
			Name:    "a",
			Status:  Fail,
			Code:    contract.Unauthorized,
			Message: "rejected s3cret-value",
			Detail:  map[string]any{"who": "s3cret-value", "deep": map[string]any{"list": []string{"x s3cret-value"}}},
		}
	}}

	report := runSteps(t, time.Minute, leaky)

	fields := coded(t, report).(contract.Fielded).ErrorFields()
	inError, _ := json.Marshal(fields)
	inReport, _ := json.Marshal(report)
	if text := string(inError) + string(inReport); strings.Contains(text, "s3cret-value") || !strings.Contains(text, "x [redacted]") {
		t.Errorf("report and the error's doctor field: %s", text)
	}
}
