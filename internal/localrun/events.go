package localrun

import (
	"encoding/json"
	"io"
	"math"

	"github.com/selectiveci/selective-cli/internal/format"
)

func buildStartFields(tree json.RawMessage) map[string]any {
	fields := map[string]any{"local": true}
	var snapshot struct {
		SHA   string   `json:"sha"`
		Dirty []string `json:"dirty"`
	}
	if json.Unmarshal(tree, &snapshot) == nil && snapshot.SHA != "" {
		fields["commit"] = snapshot.SHA
		fields["dirty"] = len(snapshot.Dirty) > 0
	}
	return fields
}

func selectionFields(s Selection, sum Summary, total int) map[string]any {
	fields := map[string]any{
		"outcome":       s.Outcome,
		"cause":         nilIfEmpty(s.Cause),
		"selected":      sum.Selected,
		"skipped":       sum.Skipped,
		"total":         total,
		"changed_files": s.ChangedFiles,
	}
	if s.Source != "" {
		fields["source"] = s.Source
	}
	return fields
}

func caseFields(id, file string, line int, seconds float64) map[string]any {
	return map[string]any{
		"node": "local",
		"id":   id,
		"file": file,
		"line": line,
		"ms":   int64(math.Round(seconds * 1000)),
	}
}

func (r *run) progressFields() map[string]any {
	return map[string]any{
		"completed": r.completed,
		"total":     r.total,
		"percent":   r.completed * 100 / r.total,
		"failed":    r.sum.Failed,
	}
}

func summaryFields(s Summary) map[string]any {
	return map[string]any{
		"passed":   s.Passed,
		"failed":   s.Failed,
		"pending":  s.Pending,
		"selected": s.Selected,
		"skipped":  s.Skipped,
		"ms":       s.MS,
	}
}

func WriteInterruptedSummary(events io.Writer, sum Summary) error {
	fields := summaryFields(sum)
	fields["interrupted"] = true
	return format.EventLine(events, "summary", fields)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
