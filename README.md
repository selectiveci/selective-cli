# sel

The Selective CLI, `sel`: stream live test results from your CI builds to your
terminal or your agent.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/selectiveci/selective-cli/main/install.sh | sh
```

Or build from source with Go 1.23+: `go build -o sel ./cmd/sel`.

## Log in

```bash
sel auth login
```

This prints a one-time code and opens your browser; approve the request there
and you're logged in. When there's no browser (SSH, containers, remote boxes),
it prints the URL to open on any machine where you're signed in to Selective.
It's the OAuth 2.0 device authorization grant (RFC 8628), the same flow as
`gh auth login`.

- **Where the token lives.** In your system keychain (macOS Keychain, Linux
  Secret Service, Windows Credential Manager). Without one, it goes in
  `~/.config/selective/config.json` with mode 0600, and the CLI says so.
- **Revoking it.** It appears on your API tokens page as
  "CLI: selective-cli <version> on <hostname>", where you can revoke it.
  `sel auth logout` revokes it too.
- **Other servers.** Use `--hostname`: `sel auth login --hostname
  staging.selective.ci`, or `--hostname localhost:4000` for a local dev
  server (loopback hosts default to http).

```bash
sel auth status    # who you're logged in as, and whether the token still works
sel auth logout    # revoke this machine's token and forget it
```

### CI and agents

Don't log in: create a token on the API tokens page and provide it through
the environment.

| Variable | Meaning |
|---|---|
| `SELECTIVE_TOKEN` | API token to use. Takes precedence over any stored login and is never written to disk. |
| `SELECTIVE_HOST` | Server, when it isn't app.selective.ci. The runner gems' `wss://` form works too. |
| `SELECTIVE_CONFIG_DIR` | Where to keep config (default `~/.config/selective`). |

To store a token non-interactively, pass it on stdin. It never goes on the
command line, where it would land in shell history and `ps`.

```bash
sel auth login --with-token < token.txt
```

## Set up a repository

What an agent (or you) needs to put a repository on Selective, without
opening the dashboard:

```bash
sel auth signup                 # new account: signup page, then logged in with setup access
sel auth login --setup          # existing account; also allows creating suites
sel orgs                        # your organizations (pass --org when you have several)
sel repos                       # repositories Selective's GitHub App can see
sel suites create --name web-rspec --framework rspec    # repo defaults to this checkout's origin
sel suites key web-rspec --github-secret SELECTIVE_API_KEY
```

- **For agents.** `sel auth login --setup --json` writes
  `{"event":"authorize","url":…,"user_code":…}` to stdout at once and
  `{"event":"logged_in",…}` the moment the person approves. Run it in the
  background, hand the person the URL, and wait for the second line. Without an
  account, the page signs them up with GitHub first.
- **`suites create` is repeatable.** The same name returns the same suite.
  Names are unique within an organization, so include the repository in the
  name when the organization has several.
- **The API key is never shown.** `suites key` needs a destination: a GitHub
  Actions secret through `gh` (key on stdin) with `--github-secret NAME`, or
  `--stdout` piped into a secret store:
  `sel suites key web-rspec --stdout | <your CI's secret command>`.
  `--stdout` refuses a terminal.
- **Output.** JSON when piped or with `--json`, tables on a terminal.
- `SELECTIVE_ORG` sets the organization for these commands.

## Tail a build

```bash
sel tail                   # the build for this repo's HEAD commit
sel tail --run-id <id>     # a specific build
```

Output is NDJSON when piped and readable when on a terminal
(`--format ndjson|pretty`). Exit codes:

| Code | Meaning |
|---|---|
| 0 | all tests passed |
| 1 | real test failures |
| 2 | only flaky failures |
| 3 | no build found for this commit |
| 4 | authentication or network error |
