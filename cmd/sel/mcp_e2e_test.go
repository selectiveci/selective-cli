package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/localrun"
)

const (
	e2eSeedVariable = "SELECTIVE_E2E_MCP_SEED"
	e2eVenvVariable = "SELECTIVE_E2E_MCP_VENV"
	timeoutSeconds  = 2
	x14Bound        = timeoutSeconds*time.Second + localrun.ShutdownBudget + time.Second
)

type e2eSeed struct {
	Host       string   `json:"host"`
	Org        string   `json:"org"`
	Keep       string   `json:"keep"`
	Pool       []string `json:"pool"`
	RunSuite   string   `json:"run_suite"`
	WriteToken string   `json:"write_token"`
	ReadToken  string   `json:"read_token"`
}

var nextControl atomic.Int32

func e2eSetup(t *testing.T) (e2eSeed, string) {
	t.Helper()
	path := os.Getenv(e2eSeedVariable)
	if path == "" {
		t.Skipf("set %s to a seed file to run the stdio end-to-end tests", e2eSeedVariable)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s=%s: %v", e2eSeedVariable, path, err)
	}
	var seed e2eSeed
	if err := json.Unmarshal(body, &seed); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if seed.WriteToken == "" || seed.ReadToken == "" || seed.Org == "" || seed.Keep == "" || seed.RunSuite == "" || len(seed.Pool) == 0 {
		t.Fatalf("%s lacks a token, the org or the suites", path)
	}
	address, err := url.Parse(seed.Host)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", address.Host, 3*time.Second)
	if err != nil {
		t.Fatalf("the server at %s is not up: %v", seed.Host, err)
	}
	_ = conn.Close()
	binary := filepath.Join(t.TempDir(), "sel")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build sel: %v\n%s", err, out)
	}
	return seed, binary
}

func startMCP(t *testing.T, seed e2eSeed, binary, token, dir string, extraEnv []string, flags ...string) *mcp.ClientSession {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"mcp"}, flags...)...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SELECTIVE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env,
		"HOME="+t.TempDir(),
		"SELECTIVE_CONFIG_DIR="+t.TempDir(),
		"SELECTIVE_HOST="+seed.Host,
		"SELECTIVE_ORG="+seed.Org,
		"SELECTIVE_TOKEN="+token,
		"SELECTIVE_NO_UPDATE_NOTIFIER=1",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("start sel mcp %v: %v", flags, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolCount(t *testing.T, session *mcp.ClientSession) int {
	t.Helper()
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(list.Tools)
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	return session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
}

func exitCodeOf(res *mcp.CallToolResult) any {
	selective, _ := res.Meta["selective"].(map[string]any)
	return selective["exit_code"]
}

func expectRefused(t *testing.T, mode string, session *mcp.ClientSession, name string, args map[string]any) {
	t.Helper()
	res, err := callTool(t, session, name, args)
	var wire *jsonrpc.Error
	if res != nil || !errors.As(err, &wire) || wire.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("%s: calling hidden %s: result = %+v, err = %v, want an invalid-params protocol error and no result", mode, name, res, err)
	}
}

func TestMCPStdioE2EListsEachModeAndRelaysRealServerResults(t *testing.T) {
	seed, binary := e2eSetup(t)
	cwd := t.TempDir()
	byDefault := startMCP(t, seed, binary, seed.WriteToken, cwd, nil)
	allowed := startMCP(t, seed, binary, seed.WriteToken, cwd, nil, "--allow-destructive")
	readOnly := startMCP(t, seed, binary, seed.WriteToken, cwd, nil, "--read-only")
	for name, c := range map[string]struct {
		session *mcp.ClientSession
		want    int
	}{"default": {byDefault, 30}, "allow-destructive": {allowed, 33}, "read-only": {readOnly, 19}} {
		if got := toolCount(t, c.session); got != c.want {
			t.Errorf("%s lists %d tools, want %d", name, got, c.want)
		}
	}

	orgs, err := callTool(t, byDefault, "orgs", nil)
	if err != nil || orgs.IsError || exitCodeOf(orgs) != float64(0) || !strings.Contains(fmt.Sprint(orgs.StructuredContent), seed.Org) {
		t.Errorf("orgs: err = %v, result = %+v, want success listing %s", err, orgs, seed.Org)
	}

	unknown, err := callTool(t, byDefault, "suites", map[string]any{"org": "no-such-org-sel285"})
	if err != nil || !unknown.IsError || exitCodeOf(unknown) != float64(3) {
		t.Errorf("unknown org: err = %v, result = %+v, want isError with exit_code 3", err, unknown)
	}

	denied := startMCP(t, seed, binary, seed.ReadToken, cwd, nil)
	created, err := callTool(t, denied, "suites_create", map[string]any{"name": "c3-denied-" + strconv.FormatInt(time.Now().UnixNano(), 36)})
	if err != nil || !created.IsError || exitCodeOf(created) != float64(6) {
		t.Errorf("read-only token on suites_create: err = %v, result = %+v, want isError with exit_code 6", err, created)
	}

	expectRefused(t, "default", byDefault, "suites_delete", map[string]any{"suite": seed.Keep, "yes": true})
	kept, err := callTool(t, byDefault, "suites_show", map[string]any{"suite": seed.Keep})
	if err != nil || kept.IsError || !strings.Contains(fmt.Sprint(kept.StructuredContent), seed.Keep) {
		t.Errorf("suites_show %s after the refused delete: err = %v, result = %+v, want the suite still there", seed.Keep, err, kept)
	}

	readOrgs, err := callTool(t, readOnly, "orgs", nil)
	if err != nil || readOrgs.IsError || exitCodeOf(readOrgs) != float64(0) {
		t.Errorf("read-only: orgs: err = %v, result = %+v, want it to run", err, readOrgs)
	}
	expectRefused(t, "read-only", readOnly, "suites_create", map[string]any{"name": "c3-hidden-" + strconv.FormatInt(time.Now().UnixNano(), 36)})
	expectRefused(t, "read-only", readOnly, "suites_delete", map[string]any{"suite": seed.Keep, "yes": true})
	expectRefused(t, "read-only", readOnly, "test", nil)
	expectRefused(t, "allow-destructive", allowed, "help", nil)
	expectRefused(t, "allow-destructive", allowed, "mcp", nil)

	index := int(nextControl.Add(1)) - 1
	if index >= len(seed.Pool) {
		t.Fatalf("the seed has %d control suites and this run needs number %d", len(seed.Pool), index+1)
	}
	control := seed.Pool[index]
	deleted, err := callTool(t, allowed, "suites_delete", map[string]any{"suite": control, "yes": true})
	if err != nil || deleted.IsError {
		t.Fatalf("control: suites_delete %s with --allow-destructive: err = %v, result = %+v, want it to delete", control, err, deleted)
	}
	gone, err := callTool(t, allowed, "suites_show", map[string]any{"suite": control})
	if err != nil || !gone.IsError || exitCodeOf(gone) != float64(3) {
		t.Errorf("suites_show %s after the control delete: err = %v, result = %+v, want not found (exit 3)", control, err, gone)
	}
}

func signalIgnoringProject(t *testing.T) (string, string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "ignorer")
	root := t.TempDir()
	files := map[string]string{
		"pytest.ini":                    "[pytest]\npythonpath = .\naddopts = -p no:cacheprovider\n",
		"calc.py":                       "def add(a, b):\n    return a + b\n",
		"tests/test_calc.py":            "from calc import add\n\n\ndef test_add():\n    assert add(1, 2) == 3\n",
		"tests/test_ignores_signals.py": "import os\nimport signal\nimport time\n\n\ndef test_ignores_signals():\n    signal.signal(signal.SIGINT, signal.SIG_IGN)\n    signal.signal(signal.SIGTERM, signal.SIG_IGN)\n    with open(os.environ[\"E2E_IGNORER_FILE\"], \"w\") as handle:\n        handle.write(\"%d %d\" % (os.getpid(), os.getpgid(0)))\n    time.sleep(600)\n",
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(root, "calc.py"), []byte("def add(a, b):\n    return b + a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, pidFile
}

func processGroupMembers(t *testing.T, pgid int) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var members []string
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) > 2 && fields[2] == strconv.Itoa(pgid) && fields[0] != "Z" {
			members = append(members, fmt.Sprintf("%d (%s)", pid, strings.TrimSpace(string(stat[:strings.IndexByte(string(stat), '(')]))))
		}
	}
	return members
}

func TestMCPStdioE2ETestToolTimeoutKillsARunnerThatIgnoresSignalsWithinTheBudget(t *testing.T) {
	seed, binary := e2eSetup(t)
	venv := os.Getenv(e2eVenvVariable)
	if venv == "" {
		t.Fatalf("%s must name a virtualenv with the selective pytest client", e2eVenvVariable)
	}
	if _, err := os.Stat(filepath.Join(venv, "bin", "selective")); err != nil {
		t.Fatalf("no selective pytest client in %s: %v", venv, err)
	}
	root, pidFile := signalIgnoringProject(t)
	session := startMCP(t, seed, binary, seed.WriteToken, root, []string{
		"PATH=" + filepath.Join(venv, "bin") + ":" + os.Getenv("PATH"),
		"VIRTUAL_ENV=" + venv,
		"E2E_IGNORER_FILE=" + pidFile,
	})
	started := time.Now()
	res, err := callTool(t, session, "test", map[string]any{"against": "HEAD", "timeout_s": timeoutSeconds, "suite": seed.RunSuite})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("test tool: %v", err)
	}
	t.Logf("test tool returned after %s (bound %s): isError=%v meta=%v", elapsed, x14Bound, res.IsError, res.Meta)
	if !res.IsError || exitCodeOf(res) != float64(8) {
		t.Errorf("result = %+v, want a timeout tool error with exit_code 8", res)
	}
	if elapsed >= x14Bound {
		t.Errorf("the call took %s, want under %s (timeout_s %d + localrun.ShutdownBudget %s + 1s)", elapsed, x14Bound, timeoutSeconds, localrun.ShutdownBudget)
	}
	body, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the signal-ignoring test never ran: %v\n%v", err, res.StructuredContent)
	}
	var pid, pgid int
	if _, err := fmt.Sscanf(string(body), "%d %d", &pid, &pgid); err != nil {
		t.Fatal(err)
	}
	if left := processGroupMembers(t, pgid); len(left) != 0 {
		t.Errorf("process group %d still has %v after the call returned", pgid, slices.Clone(left))
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		t.Errorf("the signal-ignoring test process %d is still alive", pid)
	}
}
