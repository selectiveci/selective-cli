package runs

func walk[T any](cursor string, all bool, fetch func(cursor string) ([]T, *string, error)) ([]T, *string, bool, error) {
	var rows []T
	var next *string
	for requests := 0; requests < maxRequests; requests++ {
		page, nextCursor, err := fetch(cursor)
		if err != nil {
			return nil, nil, false, err
		}
		rows = append(rows, page...)
		next = nextCursor
		if !all || next == nil || len(rows) >= maxFailures {
			break
		}
		cursor = *next
	}
	return rows, next, all && next != nil, nil
}
