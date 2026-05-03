package auth

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/selectiveci/selective-cli/internal/config"
)

type MeResponse struct {
	UserID   string   `json:"user_id"`
	Email    string   `json:"email"`
	OrgSlugs []string `json:"org_slugs"`
}

func Validate(baseURL, token string) (*MeResponse, error) {
	req, err := http.NewRequest("GET", baseURL+"/api/v1/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("invalid token")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var me MeResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}
	return &me, nil
}

func PromptToken() (string, error) {
	fmt.Fprint(os.Stderr, "Paste your API token (from https://app.selective.ci/users/settings/tokens): ")
	reader := bufio.NewReader(os.Stdin)
	token, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(token), nil
}

func Run(token string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("could not load config: %w", err)
	}

	if token == "" {
		token, err = PromptToken()
		if err != nil {
			return err
		}
	}

	if !strings.HasPrefix(token, "sk_live_") {
		return fmt.Errorf("token must start with sk_live_")
	}

	me, err := Validate(cfg.APIBase(), token)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}

	cfg.Token = token
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authenticated as %s (orgs: %s)\n", me.Email, strings.Join(me.OrgSlugs, ", "))
	return nil
}
