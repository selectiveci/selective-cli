package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"time"
)

const (
	refreshAfter = 24 * time.Hour
	retryAfter   = time.Hour
)

var (
	releasesURL    = releasesPage
	releasesAPIURL = "https://api.github.com/repos/selectiveci/selective-cli/releases/latest"
	checkTimeout   = 2 * time.Second
	ciVariables    = []string{"CI", "GITHUB_ACTIONS", "BUILD_NUMBER", "RUN_ID", "CODESPACES"}
)

const OptOutVariable = "SELECTIVE_NO_UPDATE_NOTIFIER"

func Enabled(getenv func(string) string) bool {
	if getenv(OptOutVariable) != "" {
		return false
	}
	for _, name := range ciVariables {
		if getenv(name) != "" {
			return false
		}
	}
	return true
}

func due(st State, version string, at time.Time) bool {
	if !isSemver(version) {
		return false
	}
	if st.FailedAt != nil && at.Sub(*st.FailedAt) < retryAfter {
		return false
	}
	return st.CheckedAt == nil || at.Sub(*st.CheckedAt) >= refreshAfter
}

func fetchLatest(ctx context.Context) (string, error) {
	client := &http.Client{
		Transport:     Transport(nil),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	version, err := latestFromRedirect(ctx, client)
	if err == nil || ctx.Err() != nil {
		return version, err
	}
	return latestFromAPI(ctx, client)
}

func latestFromRedirect(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, releasesURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode < 300 || resp.StatusCode > 399 || path.Base(path.Dir(location.Path)) != "tag" {
		return "", fmt.Errorf("no release redirect (HTTP %d)", resp.StatusCode)
	}
	if version := cleanVersion(path.Base(location.Path)); version != "" {
		return version, nil
	}
	return "", errors.New("release redirect is not a version")
}

func latestFromAPI(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPIURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		TagName string `json:"tag_name"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || cleanVersion(body.TagName) == "" {
		return "", fmt.Errorf("no release in the API response (HTTP %d)", resp.StatusCode)
	}
	return cleanVersion(body.TagName), nil
}

type Checker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func StartCheck(version string, getenv func(string) string) *Checker {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Checker{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		if Enabled(getenv) && due(LoadState(), version, time.Now()) {
			refresh(ctx)
		}
	}()
	return c
}

func (c *Checker) Stop() {
	c.cancel()
	<-c.done
}

func refresh(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, checkTimeout)
	defer cancel()
	latest, err := fetchLatest(ctx)
	if parent.Err() != nil {
		return
	}
	modifyState(func(st *State) {
		if err != nil {
			st.FailedAt = now()
			return
		}
		st.LatestVersion, st.CheckedAt, st.FailedAt = latest, now(), nil
	})
}

func Notice(info Info) string {
	var headline string
	switch {
	case info.BelowMinimum != nil && *info.BelowMinimum:
		headline = fmt.Sprintf("sel %s is no longer supported: this server needs %s or later", info.Version, info.MinVersion)
	case info.UpdateAvailable != nil && *info.UpdateAvailable:
		headline = fmt.Sprintf("A new release of sel is available: %s → %s", info.Version, info.LatestVersion)
	default:
		return ""
	}
	releases := info.URL
	if releases == "" {
		releases = releasesPage
	}
	return fmt.Sprintf("\n%s\nTo upgrade, run: %s\n%s\n", headline, UpgradeCommand, releases)
}
