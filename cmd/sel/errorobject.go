package main

import (
	"io"
	"maps"
	"slices"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/doctor"
	"github.com/selectiveci/selective-cli/internal/format"
)

func writeErrorObject(sink, stderr io.Writer, coded contract.Coded) {
	if _, underTimeout := sink.(*guard); !underTimeout {
		_ = format.Error(sink, coded)
		return
	}
	shown, report := withoutDoctor(coded)
	_ = format.BoundedError(sink, shown)
	if report != nil {
		_ = format.Resource(stderr, "doctor", report)
	}
}

func withoutDoctor(coded contract.Coded) (contract.Coded, *doctor.Report) {
	fielded, ok := coded.(contract.Fielded)
	if !ok {
		return coded, nil
	}
	report, ok := fielded.ErrorFields()["doctor"].(*doctor.Report)
	if !ok {
		return coded, nil
	}
	fields := maps.Clone(fielded.ErrorFields())
	delete(fields, "doctor")
	if i := slices.IndexFunc(report.Checks, func(c doctor.Check) bool { return c.Status == doctor.Fail }); i >= 0 {
		fields["failed_check"] = report.Checks[i].Name
	}
	return &contract.Error{Code: coded.ErrorCode(), Message: coded.Error(), Fields: fields}, report
}
