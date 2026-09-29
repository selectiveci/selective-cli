package format

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"os"
)

type Event map[string]any

// DefaultFormat returns "pretty" if stdout is a TTY, otherwise "ndjson".
func DefaultFormat() string {
	if isatty.IsTerminal(os.Stdout.Fd()) {
		return "pretty"
	}
	return "ndjson"
}

// Writer writes events to w in the chosen format.
type Writer struct {
	w      io.Writer
	format string

	passStyle  lipgloss.Style
	failStyle  lipgloss.Style
	nodeColors map[string]lipgloss.Style
}

var nodeColorPool = []lipgloss.Color{
	"#60a5fa", "#34d399", "#f59e0b", "#f472b6", "#a78bfa", "#38bdf8",
}

func NewWriter(w io.Writer, format string) *Writer {
	return &Writer{
		w:          w,
		format:     format,
		passStyle:  lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")),
		failStyle:  lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")),
		nodeColors: make(map[string]lipgloss.Style),
	}
}

func (fw *Writer) Write(raw []byte) error {
	if fw.format == "ndjson" {
		_, err := fmt.Fprintf(fw.w, "%s\n", strings.TrimRight(string(raw), "\n"))
		return err
	}
	return fw.writePretty(raw)
}

func (fw *Writer) writePretty(raw []byte) error {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		_, err = fmt.Fprintln(fw.w, string(raw))
		return err
	}

	switch ev["event"] {
	case "build_start":
		buildID, _ := ev["build_id"].(string)
		commit, _ := ev["commit"].(string)
		fmt.Fprintf(fw.w, "\n  Build %s  commit %s\n\n", buildID, shortSHA(commit))

	case "test_pass":
		node := fw.nodeStyle(ev["node"])
		file, _ := ev["file"].(string)
		line, _ := ev["line"].(float64)
		ms, _ := ev["ms"].(float64)
		fmt.Fprintf(fw.w, "  %s  %s  %s:%d  %dms\n",
			fw.passStyle.Render("✓"),
			node,
			file, int(line), int(ms))

	case "test_fail":
		node := fw.nodeStyle(ev["node"])
		file, _ := ev["file"].(string)
		line, _ := ev["line"].(float64)
		errMsg, _ := ev["error"].(string)
		fmt.Fprintf(fw.w, "  %s  %s  %s:%d\n    %s\n",
			fw.failStyle.Render("✗"),
			node,
			file, int(line),
			fw.failStyle.Render(errMsg))

	case "summary":
		passed, _ := ev["passed"].(float64)
		failed, _ := ev["failed"].(float64)
		ms, _ := ev["ms"].(float64)
		fmt.Fprintf(fw.w, "\n  %s  %d passed  %d failed  %.1fs\n\n",
			summaryIcon(int(failed)),
			int(passed), int(failed), ms/1000)
	}
	return nil
}

func (fw *Writer) nodeStyle(nodeRaw any) string {
	node, _ := nodeRaw.(string)
	if style, ok := fw.nodeColors[node]; ok {
		return style.Render(node)
	}
	idx := len(fw.nodeColors) % len(nodeColorPool)
	style := lipgloss.NewStyle().Foreground(nodeColorPool[idx])
	fw.nodeColors[node] = style
	return style.Render(node)
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func summaryIcon(failed int) string {
	if failed == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")).Render("●")
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Render("●")
}
