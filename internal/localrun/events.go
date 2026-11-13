package localrun

import (
	"encoding/json"
	"math"
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

func (r *run) summaryFields() map[string]any {
	return map[string]any{
		"passed":   r.sum.Passed,
		"failed":   r.sum.Failed,
		"pending":  r.sum.Pending,
		"selected": r.sum.Selected,
		"skipped":  r.sum.Skipped,
		"ms":       r.sum.MS,
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
