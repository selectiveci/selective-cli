package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/contract"
)

const stderrCap = 8 * 1024

type tail struct{ buf []byte }

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	t.buf = append(t.buf, p[max(0, n-stderrCap):]...)
	if over := len(t.buf) - stderrCap; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return n, nil
}

func (r *Runner) Handler() mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now()
		res, err := r.Run(ctx, req.Params.Name, req.Params.Arguments)
		if err != nil {
			return nil, err
		}
		return toolResult(r.commands[req.Params.Name].Path, res, time.Since(started)), nil
	}
}

func toolResult(command string, res Result, elapsed time.Duration) *mcp.CallToolResult {
	exit := res.ExitCode
	if res.TimedOut {
		exit = contract.ExitTimeout
	}
	lines, last := parseStdout(res.Stdout)
	isError := !isOutcome(exit, last)
	synthesized := isError && !isReal(last)
	code, _ := last["error"].(string)
	if synthesized {
		var line json.RawMessage
		line, code = synthesize(command, exit, res.Stderr)
		lines = append(lines, line)
	}
	var body any = map[string]any{"events": lines, "truncated": res.Truncated}
	if len(lines) == 1 && last["event"] == nil {
		body = lines[0]
	}
	text, _ := json.Marshal(body)
	selective := map[string]any{
		"exit_code":   exit,
		"retryable":   isError && contract.RetryableCode(contract.Code(code)),
		"duration_ms": elapsed.Milliseconds(),
		"command":     command,
	}
	if synthesized {
		selective["synthesized"] = true
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(text)}},
		StructuredContent: body,
		IsError:           isError,
		Meta:              mcp.Meta{"selective": selective},
	}
}

func parseStdout(stdout []byte) ([]json.RawMessage, map[string]any) {
	lines := []json.RawMessage{}
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		if line = bytes.TrimSpace(line); json.Valid(line) {
			lines = append(lines, line)
		}
	}
	var last map[string]any
	if len(lines) > 0 {
		_ = json.Unmarshal(lines[len(lines)-1], &last)
	}
	return lines, last
}

func isOutcome(exit int, last map[string]any) bool {
	switch exit {
	case contract.ExitOK:
		return true
	case contract.ExitFailures, contract.ExitFlaky:
		return last["event"] == "summary"
	case contract.ExitPending:
		return last["approval"] != nil || last["result"] == "pending_approval"
	}
	return false
}

func isReal(last map[string]any) bool {
	_, isCode := last["error"].(string)
	return isCode || last["event"] == "summary"
}

func synthesize(command string, exit int, stderr []byte) (json.RawMessage, string) {
	code, ok := contract.DefaultCode(exit)
	if !ok {
		code = contract.InternalError
	}
	message := strings.ToValidUTF8(strings.TrimSpace(string(stderr)), "")
	if message == "" {
		message = fmt.Sprintf("%s exited %d", command, exit)
	}
	line, _ := json.Marshal(map[string]any{"v": 1, "error": code, "message": message})
	return line, string(code)
}
