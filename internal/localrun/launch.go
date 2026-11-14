package localrun

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type Framework string

const (
	RSpec    Framework = "rspec"
	Minitest Framework = "minitest"
	Pytest   Framework = "pytest"
)

const minRubyCore = "0.3.0"

var goos = runtime.GOOS

func refuseWindows() error {
	if goos == "windows" {
		return contract.Errorf(contract.UsageError, "sel test does not run on Windows yet")
	}
	return nil
}

type marker struct{ file, needle string }

var detectors = []struct {
	framework Framework
	markers   []marker
}{
	{RSpec, []marker{{"Gemfile", "rspec-core"}, {"Gemfile.lock", "rspec-core"}}},
	{Minitest, []marker{{"Gemfile", "selective-ruby-minitest"}, {"Gemfile.lock", "selective-ruby-minitest"}}},
	{Pytest, []marker{{"pyproject.toml", "pytest"}, {"pytest.ini", ""}, {"setup.cfg", "pytest"}, {"tox.ini", "pytest"}}},
}

var rubyCoreSpec = regexp.MustCompile(`(?m)^\s+selective-ruby-core \((\d+)\.(\d+)\.(\d+)(\.?[A-Za-z]\w*)?[^)]*\)`)

func DetectFramework(root, explicit string) (Framework, error) {
	if err := refuseWindows(); err != nil {
		return "", err
	}
	if explicit != "" {
		for _, d := range detectors {
			if string(d.framework) == explicit {
				return d.framework, nil
			}
		}
		return "", contract.Errorf(contract.UsageError, "unknown framework %q; sel test supports %s", explicit, supported())
	}
	var found []string
	var framework Framework
	for _, d := range detectors {
		if hasMarker(root, d.markers) {
			found = append(found, string(d.framework))
			framework = d.framework
		}
	}
	switch len(found) {
	case 1:
		return framework, nil
	case 0:
		return "", contract.Errorf(contract.NoFramework, "no supported test framework found in %s (looked for %s); pass --framework to choose one", root, supported())
	}
	return "", contract.Errorf(contract.NoFramework, "found several test frameworks in %s: %s; pass --framework to choose one", root, strings.Join(found, ", "))
}

func supported() string {
	names := make([]string, len(detectors))
	for i, d := range detectors {
		names[i] = string(d.framework)
	}
	return strings.Join(names, ", ")
}

func hasMarker(root string, markers []marker) bool {
	for _, m := range markers {
		if body, err := os.ReadFile(filepath.Join(root, m.file)); err == nil && strings.Contains(string(body), m.needle) {
			return true
		}
	}
	return false
}

func ClientCommand(root string, fw Framework, env []string) ([]string, error) {
	if err := refuseWindows(); err != nil {
		return nil, err
	}
	if fw != Pytest {
		if err := checkRubyCore(root); err != nil {
			return nil, err
		}
		return []string{"bundle", "exec", "selective", string(fw)}, nil
	}
	client, ok := findPytestClient(root, env)
	if !ok {
		return nil, contract.Errorf(contract.RunnerMissing, "sel test could not find the selective pytest client; install selective-pytest in the project's virtualenv")
	}
	return []string{client, "pytest"}, nil
}

func checkRubyCore(root string) error {
	found := "none"
	lock, _ := os.ReadFile(filepath.Join(root, "Gemfile.lock"))
	if m := rubyCoreSpec.FindStringSubmatch(string(lock)); m != nil {
		found = strings.Join(m[1:4], ".") + m[4]
		if !olderThan(m[1:], minRubyCore) {
			return nil
		}
	}
	return contract.Errorf(contract.RunnerMissing, "sel test needs selective-ruby-core %s or newer (Gemfile.lock has %s)", minRubyCore, found)
}

func olderThan(version []string, minimum string) bool {
	for i, want := range strings.Split(minimum, ".") {
		have, _ := strconv.Atoi(version[i])
		need, _ := strconv.Atoi(want)
		if have != need {
			return have < need
		}
	}
	return version[3] != ""
}

func findPytestClient(root string, env []string) (string, bool) {
	var dirs []string
	if venv := envValue(env, "VIRTUAL_ENV"); venv != "" {
		dirs = append(dirs, filepath.Join(venv, "bin"))
	}
	dirs = append(dirs, filepath.Join(root, ".venv", "bin"), filepath.Join(root, "venv", "bin"))
	dirs = append(dirs, filepath.SplitList(envValue(env, "PATH"))...)
	for _, dir := range dirs {
		path := filepath.Join(dir, "selective")
		if info, err := os.Stat(path); err != nil || info.Mode()&0o111 == 0 {
			continue
		}
		if script, err := os.ReadFile(path); err == nil && strings.Contains(string(script), "selective_pytest") {
			return path, true
		}
	}
	return "", false
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], key+"="); ok {
			return value
		}
	}
	return ""
}

var ErrInterrupted = errors.New("interrupted")

const (
	defaultGrace                = 5 * time.Second
	noSignal     syscall.Signal = 0
)

type Launch struct {
	Root      string
	Framework Framework
	Args      []string
	Sel       string
	Stderr    io.Writer
	Grace     time.Duration
}

type client struct {
	cmd    *exec.Cmd
	exited chan struct{}
	grace  time.Duration
}

func (l Launch) Run(ctx context.Context, events io.Writer, opts Options) (Summary, error) {
	if err := refuseWindows(); err != nil {
		return Summary{}, err
	}
	argv, err := ClientCommand(l.Root, l.Framework, os.Environ())
	if err != nil {
		return Summary{}, err
	}
	cmd := exec.Command(argv[0], append(argv[1:], l.Args...)...)
	if err := isolate(cmd); err != nil {
		return Summary{}, err
	}
	seat, err := NewSeat(l.Sel)
	if err != nil {
		return Summary{}, contract.Errorf(contract.InternalError, "could not set up the transport seat: %v", err)
	}
	defer seat.Close()
	cmd.Dir = l.Root
	cmd.Env = childEnv(os.Environ(), seat.Env(), l.Root)
	output := cmp.Or[io.Writer](l.Stderr, os.Stderr)
	cmd.Stdout, cmd.Stderr = output, output
	c := &client{cmd: cmd, exited: make(chan struct{}), grace: cmp.Or(l.Grace, defaultGrace)}
	cmd.WaitDelay = c.grace
	if err := cmd.Start(); err != nil {
		return Summary{}, contract.Errorf(contract.RunnerMissing, "sel test could not start %s: %v", argv[0], err)
	}
	go func() {
		_ = cmd.Wait()
		close(c.exited)
	}()
	defer c.sweep()
	return c.drive(ctx, seat, events, opts)
}

func (c *client) drive(ctx context.Context, seat *Seat, events io.Writer, opts Options) (Summary, error) {
	acceptCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		select {
		case <-c.exited:
			stop()
		case <-acceptCtx.Done():
		}
	}()
	conn, err := seat.Accept(acceptCtx)
	if err != nil {
		if ctx.Err() != nil {
			c.reap(syscall.SIGINT, syscall.SIGTERM)
			return Summary{}, ErrInterrupted
		}
		return Summary{}, contract.Errorf(contract.RunnerError, "the test runner exited before it connected to sel (%s); its output is above", c.cmd.ProcessState)
	}
	defer conn.Close()
	sum, err := Run(ctx, conn, conn, events, opts)
	switch {
	case errors.Is(err, context.Canceled):
		c.reap(syscall.SIGINT, syscall.SIGTERM)
		return sum, ErrInterrupted
	case err != nil:
		c.reap(noSignal, syscall.SIGTERM)
		return sum, err
	}
	select {
	case <-c.exited:
		return sum, nil
	case <-ctx.Done():
		c.reap(syscall.SIGINT, syscall.SIGTERM)
		return sum, ErrInterrupted
	case <-time.After(c.grace):
		c.reap(syscall.SIGTERM)
		return sum, contract.Errorf(contract.RunnerError, "the test runner did not exit after reporting its finish; sel stopped it")
	}
}

func (c *client) reap(steps ...syscall.Signal) {
	for _, sig := range steps {
		if sig != noSignal {
			signalGroup(c.cmd, sig)
		}
		select {
		case <-c.exited:
			return
		case <-time.After(c.grace):
		}
	}
	signalGroup(c.cmd, syscall.SIGKILL)
	<-c.exited
}

func (c *client) sweep() {
	signalGroup(c.cmd, syscall.SIGKILL)
	<-c.exited
}

func childEnv(parent, seat []string, root string) []string {
	env := make([]string, 0, len(parent)+16)
	for _, kv := range parent {
		if !strings.HasPrefix(kv, "SELECTIVE_") {
			env = append(env, kv)
		}
	}
	env = append(env,
		"SELECTIVE_HOST=ws://localhost",
		"SELECTIVE_API_KEY=sel-local",
		"SELECTIVE_PLATFORM=sel-local",
		"SELECTIVE_RUN_ID=local-"+randomHex(),
		"SELECTIVE_RUN_ATTEMPT=1",
		"SELECTIVE_BRANCH="+gitValue(root, "symbolic-ref", "--short", "-q", "HEAD"),
		"SELECTIVE_SHA="+gitValue(root, "rev-parse", "HEAD"),
		"SELECTIVE_GIT_REPO=local/local",
		"SELECTIVE_GIT_PROVIDER=github",
	)
	return append(env, seat...)
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func gitValue(root string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if value := strings.TrimSpace(string(out)); err == nil && value != "" {
		return value
	}
	return "local"
}
