package localrun

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
