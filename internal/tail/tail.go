package tail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/credentials"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"nhooyr.io/websocket"
)

type BuildLookup struct {
	BuildID    string `json:"build_id"`
	Status     string `json:"status"`
	RunCount   int    `json:"run_count"`
	SuiteCount int    `json:"suite_count"`
}

func LookupBuild(baseURL, token, repo, sha string) (*BuildLookup, error) {
	u := fmt.Sprintf("%s/api/v1/builds/lookup?repo=%s&sha=%s", baseURL, url.QueryEscape(repo), url.QueryEscape(sha))
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil // no build found
	}
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("authentication failed — run: sel auth")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var build BuildLookup
	if err := json.NewDecoder(resp.Body).Decode(&build); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}
	return &build, nil
}

// Run executes `sel tail` with the given options.
// Returns an exit code: 0=all pass, 1=failures, 2=only flaky, 3=no build, 4=auth/network error.
func Run(runID, commitSHA, fmtFlag, hostname string) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 4, fmt.Errorf("could not load config: %w", err)
	}
	host, err := cfg.ResolveHost(hostname)
	if err != nil {
		return 4, err
	}
	cred, err := credentials.Lookup(cfg, host)
	if err != nil {
		return 4, fmt.Errorf("not logged in to %s — run: sel auth login", host)
	}

	var repo string

	if runID == "" {
		ctx, err := gitctx.Detect()
		if err != nil {
			return 4, fmt.Errorf("git detection failed: %w\n\nRun in a git repo or pass --run-id", err)
		}
		repo = ctx.Repo
		if commitSHA == "" {
			commitSHA = ctx.Commit
		}
	}

	var buildID string
	if runID != "" {
		buildID = runID
	} else {
		build, err := LookupBuild(host, cred.Token, repo, commitSHA)
		if err != nil {
			return 4, err
		}
		if build == nil {
			fmt.Fprintf(os.Stderr, "No build found for repo %q commit %s\n", repo, shortSHA(commitSHA))
			fmt.Fprintf(os.Stderr, "Push your branch and wait for CI to start, then re-run sel tail.\n")
			return 3, nil
		}
		buildID = build.BuildID
	}

	if fmtFlag == "" {
		fmtFlag = format.DefaultFormat()
	}

	return stream(host, cred.Token, buildID, fmtFlag)
}

func stream(host, token, buildID, fmtFlag string) (int, error) {
	wsURL := strings.Replace(host, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	// Phoenix serves a socket mounted at /api/v1/tail on /api/v1/tail/websocket;
	// the bare path is a 404, so tail never connected before this.
	wsURL = fmt.Sprintf("%s/api/v1/tail/websocket?build_id=%s", wsURL, url.QueryEscape(buildID))

	// The token goes in a header, not the URL: URLs end up in proxy and
	// access logs.
	header := http.Header{}
	header.Set("X-Selective-Token", token)

	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return 4, fmt.Errorf("could not connect to Selective: %w", err)
	}
	defer func() { _ = conn.CloseNow() }()

	fw := format.NewWriter(os.Stdout, fmtFlag)

	var failCount int
	var flakyCount int

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// Normal close — server closed after summary
			break
		}

		if writeErr := fw.Write(data); writeErr != nil {
			fmt.Fprintf(os.Stderr, "format error: %v\n", writeErr)
		}

		// Track outcome for exit code
		var ev map[string]any
		if jsonErr := json.Unmarshal(data, &ev); jsonErr == nil {
			switch ev["event"] {
			case "test_fail":
				failCount++
			case "summary":
				if f, ok := ev["flaky"].(float64); ok {
					flakyCount = int(f)
				}
			}
		}
	}

	conn.Close(websocket.StatusNormalClosure, "")

	if failCount > 0 && flakyCount == failCount {
		return 2, nil
	}
	if failCount > 0 {
		return 1, nil
	}
	return 0, nil
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}
