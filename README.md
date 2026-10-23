# selective

The Selective CLI: stream live test results from your CI builds to your
terminal or your agent.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/selectiveci/selective-cli/main/install.sh | sh
```

Or build from source with Go 1.23+: `go build -o selective ./cmd/selective`.

## Log in

```bash
selective auth login
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
  `selective auth logout` revokes it too.
- **Other servers.** Use `--hostname`: `selective auth login --hostname
  staging.selective.ci`, or `--hostname localhost:4000` for a local dev
  server (loopback hosts default to http).

```bash
selective auth status    # who you're logged in as, and whether the token still works
selective auth logout    # revoke this machine's token and forget it
```

### CI and agents

Don't log in: create a token on the API tokens page and provide it through
the environment.

| Variable | Meaning |
|---|---|
| `SELECTIVE_TOKEN` | API token to use. Takes precedence over any stored login and is never written to disk. |
| `SELECTIVE_HOST` | Server, when it isn't app.selective.ci. |
| `SELECTIVE_CONFIG_DIR` | Where to keep config (default `~/.config/selective`). |

To store a token non-interactively, pass it on stdin. It never goes on the
command line, where it would land in shell history and `ps`.

```bash
selective auth login --with-token < token.txt
```

## Tail a build

```bash
selective tail                   # the build for this repo's HEAD commit
selective tail --run-id <id>     # a specific build
```

Output is NDJSON when piped and readable when on a terminal
(`--format ndjson|pretty`). Each suite's run reports what test selection
decided in a `selection` event: `outcome` (`selected` or `full`), whether it
was `applied`, its `source` (`live`, `evaluation`, or `map` before the full
decision), and for a full run the `cause` (`code`, `path`, `rule`,
`message`), such as the file whose change made every test run.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | all tests passed |
| 1 | real test failures |
| 2 | only flaky failures |
| 3 | no build found for this commit |
| 4 | authentication or network error |
