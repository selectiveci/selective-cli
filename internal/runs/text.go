package runs

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const (
	unknownCell    = "unknown"
	emptyCell      = "-"
	notTrackedCell = "n/a"
)

type fields map[string]json.RawMessage

type column struct {
	header string
	key    string
	format func(float64) string
}

func decodeFields(raw []byte) (fields, error) {
	var f fields
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, contract.Errorf(contract.ServerError, "unexpected response: %v", err)
	}
	return f, nil
}

func (f fields) cell(c column) string {
	raw, ok := f[c.key]
	switch {
	case !ok:
		return unknownCell
	case string(raw) == "null":
		return emptyCell
	}
	var number float64
	if c.format != nil && json.Unmarshal(raw, &number) == nil {
		return c.format(number)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func (f fields) object(key string) fields {
	var nested fields
	_ = json.Unmarshal(f[key], &nested)
	return nested
}

func (f fields) cells(columns []column) []string {
	cells := make([]string, len(columns))
	for i, c := range columns {
		cells[i] = f.cell(c)
	}
	return cells
}

func headers(columns []column) []string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.header
	}
	return names
}

func table(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}

func percent(rate float64) string { return strconv.FormatFloat(rate*100, 'f', 1, 64) + "%" }

func seconds(s float64) string {
	return strconv.FormatFloat(math.Round(s*100)/100, 'f', -1, 64) + "s"
}

func millis(ms float64) string {
	d := time.Duration(ms) * time.Millisecond
	if d >= time.Second {
		d = d.Round(time.Second)
	}
	return d.String()
}
