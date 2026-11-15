package localrun

type buildEnv struct {
	APIKey          string `json:"api_key"`
	Host            string `json:"host"`
	Platform        string `json:"platform"`
	Branch          string `json:"branch"`
	PRTitle         string `json:"pr_title"`
	TargetBranch    string `json:"target_branch"`
	Actor           string `json:"actor"`
	SHA             string `json:"sha"`
	BaseSHA         string `json:"base_sha"`
	RunID           string `json:"run_id"`
	RunAttempt      string `json:"run_attempt"`
	RunnerID        string `json:"runner_id"`
	CommitMessage   string `json:"commit_message"`
	CommitterName   string `json:"committer_name"`
	CommitterEmail  string `json:"committer_email"`
	GitRepoFullName string `json:"git_repo_full_name"`
	GitProvider     string `json:"git_provider"`
}

func localBuildEnv() buildEnv {
	return buildEnv{
		APIKey:          "sel-local",
		Host:            "ws://localhost",
		Platform:        "sel-local",
		Branch:          "local",
		SHA:             "local",
		RunID:           "local",
		RunAttempt:      "1",
		GitRepoFullName: "local/local",
		GitProvider:     "github",
	}
}
