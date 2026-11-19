package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
			var wire *jsonrpc.Error
			if errors.As(err, &wire) || ctx.Err() != nil {
				return nil, err
			}
			res = Result{ExitCode: contract.ExitInternal, Stderr: []byte(err.Error())}
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
	overflow := len(lines) == 0 && res.Truncated && !res.TimedOut
	isError := overflow || !isOutcome(exit, last)
	synthesized := isError && (res.TimedOut || overflow || !isReal(last))
	if synthesized {
		lines = append(lines, synthesize(command, exit, res.Stderr, overflow))
	}
	var body any = map[string]any{"events": lines, "truncated": res.Truncated}
	if len(lines) == 1 && last["event"] == nil && !res.Truncated {
		body = lines[0]
	}
	text := encode(body)
	selective := map[string]any{
		"exit_code":   exit,
		"retryable":   contract.Retryable(exit),
		"duration_ms": elapsed.Milliseconds(),
		"command":     command,
	}
	if synthesized {
		selective["synthesized"] = true
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: body,
		IsError:           isError,
		Meta:              mcp.Meta{"selective": selective},
	}
}

func parseStdout(stdout []byte) ([]json.RawMessage, map[string]any) {
	lines := []json.RawMessage{}
	var last map[string]any
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		if line = bytes.TrimSpace(line); len(line) == 0 {
			continue
		}
		last = nil
		if json.Valid(line) {
			lines = append(lines, line)
			_ = json.Unmarshal(line, &last)
		}
	}
	return lines, last
}

func encode(body any) string {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
	return strings.TrimSuffix(buf.String(), "\n")
}

func isOutcome(exit int, last map[string]any) bool {
	switch exit {
	case contract.ExitOK:
		return true
	case contract.ExitFailures, contract.ExitFlaky:
		return last["event"] == "summary"
	case contract.ExitPending:
		return last["result"] == "pending_approval" || isError(last) && contract.ExitFor(contract.Code(last["error"].(string)), 0) == exit
	}
	return false
}

func isError(line map[string]any) bool {
	_, isCode := line["error"].(string)
	return isCode && line["event"] == nil
}

func isReal(last map[string]any) bool {
	return isError(last) || last["event"] == "summary"
}

func synthesize(command string, exit int, stderr []byte, overflow bool) json.RawMessage {
	code, ok := contract.DefaultCode(exit)
	if !ok || overflow {
		code = contract.InternalError
	}
	message := strings.ToValidUTF8(strings.TrimSpace(string(stderr)), "")
	if overflow {
		message = fmt.Sprintf("%s wrote no complete line within the %d byte stdout cap", command, stdoutCap)
	}
	if message == "" {
		message = fmt.Sprintf("%s exited %d", command, exit)
	}
	line, _ := json.Marshal(map[string]any{"v": 1, "error": code, "message": message})
	return line
}
