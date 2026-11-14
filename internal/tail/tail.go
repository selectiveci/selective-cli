package tail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"github.com/selectiveci/selective-cli/internal/wait"
	"nhooyr.io/websocket"
)

type Options struct {
	RunID   string
	Commit  string
	Format  string
	Wait    time.Duration
	Timeout time.Duration
}

func Run(client *api.Client, opts Options) (int, error) {
	ctx := context.Background()
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(ctx, opts.Timeout,
			contract.Errorf(contract.Timeout, "--timeout %s expired before the build finished", opts.Timeout))
		defer cancel()
	}
	if opts.Format == "" {
		opts.Format = format.DefaultFormat()
	}

	buildID := opts.RunID
	if buildID == "" {
		repo, sha, err := detect(opts.Commit)
		if err != nil {
			return refuse(err)
		}
		buildID, err = findBuild(ctx, client, repo, sha, wait.Options{
			For:    "build",
			Limit:  opts.Wait,
			Out:    waitingOutput(opts.Format),
			Pretty: opts.Format != "ndjson",
		})
		if err != nil {
			return refuse(deadlineCause(ctx, err))
		}
	}

	return stream(ctx, client.Host, client.Token, buildID, opts.Format)
}

func refuse(err error) (int, error) {
	var coded contract.Coded
	if errors.As(err, &coded) {
		return coded.ExitCode(), err
	}
	return contract.ExitInternal, err
}

func waitingOutput(format string) io.Writer {
	if format == "ndjson" {
		return os.Stdout
	}
	return os.Stderr
}

func detect(commit string) (repo, sha string, err error) {
	ctx, err := gitctx.Detect()
	if err != nil {
		return "", "", contract.Errorf(contract.GitRepoNotFound, "git detection failed: %v; run in a git repo or pass --run-id", err)
	}
	if commit == "" {
		commit = ctx.Commit
	}
	return ctx.Repo, commit, nil
}

func lookupOnce(ctx context.Context, client *api.Client, repo, sha string) (string, error) {
	build, err := client.WithContext(ctx).LookupBuild(repo, sha)
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return build.BuildID, nil
}

func findBuild(ctx context.Context, client *api.Client, repo, sha string, opts wait.Options) (string, error) {
	client = client.WithoutRetry()
	missing := contract.Errorf(contract.NoBuild, "no build for repo %q commit %s", repo, shortSHA(sha))
	if opts.Limit <= 0 {
		buildID, err := lookupOnce(ctx, client, repo, sha)
		if err == nil && buildID == "" {
			missing.Message += "; push your branch and wait for CI to start, or re-run with --wait"
			return "", missing
		}
		return buildID, err
	}

	var buildID string
	err := wait.Poll(ctx, opts, func(checkCtx context.Context) (bool, time.Duration, error) {
		var err error
		buildID, err = lookupOnce(checkCtx, client, repo, sha)
		var apiErr *api.Error
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusTooManyRequests || apiErr.Code == contract.RateLimited) {
			return false, wait.RetryAfter(err), nil
		}
		return buildID != "", 0, err
	})
	var expired *wait.ExpiredError
	if errors.As(err, &expired) {
		missing.Message += fmt.Sprintf(" within %s", expired.Limit)
		return "", missing
	}
	return buildID, err
}

func deadlineCause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

var pingInterval = 30 * time.Second

func dialError(resp *http.Response, err error) error {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return contract.Errorf(contract.Unauthorized, "Selective refused the connection (HTTP %d): the token is invalid, expired or can't see this build; run: sel auth login", status)
	case status == http.StatusNotFound:
		return contract.Errorf(contract.NotFound, "Selective has no tail for this build (HTTP 404)")
	case status >= 500:
		return contract.Errorf(contract.ServerError, "Selective failed the connection (HTTP %d)", status)
	}
	return contract.Errorf(contract.NetworkError, "could not connect to Selective: %v", err)
}

func keepAlive(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, interval time.Duration, timedOut *atomic.Bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pctx, pcancel := context.WithTimeout(ctx, interval)
		err := conn.Ping(pctx)
		expired := pctx.Err() == context.DeadlineExceeded
		pcancel()
		if err == nil {
			continue
		}
		if expired && ctx.Err() == nil {
			timedOut.Store(true)
			cancel()
		}
		return
	}
}

func stream(parent context.Context, host, token, buildID, fmtFlag string) (int, error) {
	wsURL := strings.Replace(host, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	// Phoenix serves a socket mounted at /api/v1/tail on /api/v1/tail/websocket;
	// the bare path is a 404, so tail never connected before this.
	wsURL = fmt.Sprintf("%s/api/v1/tail/websocket?build_id=%s", wsURL, url.QueryEscape(buildID))

	// The token goes in a header, not the URL: URLs end up in proxy and
	// access logs.
	header := http.Header{}
	header.Set("X-Selective-Token", token)

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if parent.Err() != nil {
			return refuse(deadlineCause(parent, parent.Err()))
		}
		return refuse(dialError(resp, err))
	}
	defer func() { _ = conn.CloseNow() }()

	interval := pingInterval
	var timedOut atomic.Bool
	go keepAlive(ctx, cancel, conn, interval, &timedOut)

	fw := format.NewWriter(os.Stdout, fmtFlag)

	var summary map[string]any
	sawFailure := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}

		if writeErr := fw.Write(data); writeErr != nil {
			fmt.Fprintf(os.Stderr, "format error: %v\n", writeErr)
		}

		var ev map[string]any
		if json.Unmarshal(data, &ev) == nil {
			switch ev["event"] {
			case "test_fail":
				sawFailure = true
			case "summary":
				summary = ev
			}
		}
	}

	conn.Close(websocket.StatusNormalClosure, "")

	if summary == nil {
		if parent.Err() != nil {
			return refuse(deadlineCause(parent, parent.Err()))
		}
		if timedOut.Load() {
			return contract.ExitRetryable, contract.Errorf(contract.NetworkError, "no response from Selective to a ping within %s; lost the connection before build %s finished", interval, buildID)
		}
		return contract.ExitRetryable, contract.Errorf(contract.NetworkError, "lost the connection to Selective before build %s finished", buildID)
	}
	return summaryExitCode(summary, sawFailure), nil
}

func summaryExitCode(summary map[string]any, sawFailure bool) int {
	failed, _ := summary["failed"].(float64)
	flaky, _ := summary["flaky"].(float64)
	switch {
	case failed > 0:
		return 1
	case flaky > 0 || sawFailure:
		return 2
	default:
		return 0
	}
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}
