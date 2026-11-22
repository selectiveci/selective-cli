package main

import (
	"encoding/json"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/doctor"
	"github.com/selectiveci/selective-cli/internal/format"
)

func writeErrorObject(sink, stderr io.Writer, coded contract.Coded) (stderrStalled bool) {
	if _, underTimeout := sink.(*guard); !underTimeout {
		_ = format.Error(sink, coded)
		return false
	}
	shown, report := forStdout(coded)
	_ = format.BoundedError(sink, shown)
	if report == nil || sameFile(sink, stderr) {
		return false
	}
	written := make(chan struct{})
	go func() {
		_ = format.Resource(stderr, "doctor", report)
		close(written)
	}()
	select {
	case <-written:
		return false
	case <-time.After(backstopExit / 2):
		return true
	}
}

func forStdout(coded contract.Coded) (contract.Coded, *doctor.Report) {
	fielded, ok := coded.(contract.Fielded)
	if !ok {
		return coded, nil
	}
	fields := maps.Clone(fielded.ErrorFields())
	if approval, has := fields["approval"]; has {
		fields["approval"] = compactApproval(approval)
	}
	report, _ := fields["doctor"].(*doctor.Report)
	if report != nil {
		delete(fields, "doctor")
		if i := slices.IndexFunc(report.Checks, func(c doctor.Check) bool { return c.Status == doctor.Fail }); i >= 0 {
			fields["failed_check"] = report.Checks[i].Name
		}
	}
	return &contract.Error{Code: coded.ErrorCode(), Message: coded.Error(), Fields: fields}, report
}

func compactApproval(approval any) any {
	var compact struct {
		ID        string  `json:"id"`
		URL       string  `json:"url"`
		Status    string  `json:"status"`
		ExpiresAt *string `json:"expires_at"`
	}
	raw, err := json.Marshal(approval)
	if err != nil || json.Unmarshal(raw, &compact) != nil {
		return approval
	}
	return compact
}
