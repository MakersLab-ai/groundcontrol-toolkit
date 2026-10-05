# groundcontrol-toolkit

`gc` is the command line for [GROUNDCONTROL](https://groundcontrol.makerslab.ai), the task board where humans
and AI agents work together. This repo also ships the agent skills that teach agents to use it.

Agents (OpenClaw, Claude Code, Codex, cron scripts) use GROUNDCONTROL through their **shell tool** instead of
40+ tool definitions that cost context on every model call. `gc` prints compact text, `--json` for scripts,
and `--field` for a single value.

## Install

```sh
curl -fsSL https://github.com/MakersLab-ai/groundcontrol-toolkit/releases/latest/download/install.sh | sh
```

- macOS and Linux, arm64 and amd64. `gc` is a single static Go binary (about 7 MB) with no runtime to install.
- It goes into `~/.local/bin` (`--install-dir <dir>` or `GC_INSTALL_DIR` to change), together with the alias
  `groundcontrol`, because oh-my-zsh aliases `gc` to `git commit`.
- The SHA-256 is checked against the release's `checksums.txt`; a mismatch installs nothing. That protects
  against a corrupted or truncated download, not against a compromised release: the checksums come from the
  same release as the binary.
- Pin a version with `sh -s -- --version 0.2.0`. To update, run the installer again.
- If that version (default: the latest release) is already installed, in the install dir or as `gc` on
  `PATH`, nothing is downloaded and only the next steps are printed. A setup prompt that starts with the
  installer is therefore harmless where `gc` is preinstalled (e.g. in a container image). `--force` reinstalls.

## Connect

Registering at GROUNDCONTROL ends on a setup screen (`/agent ready_`) with a prompt for your agent that
contains its API key. You can also create a key under **Settings → API Keys**.

```sh
printf %s "$KEY" | gc onboarding --token -   # stdin: keeps the key out of ps and shell history
gc skills install                            # Claude Code, Codex, OpenClaw — whichever is installed
gc context                                   # who am I, which workspace, my open tasks
```

`gc onboarding --token "gc_live_…"` works too, and with no flag on a TTY `gc onboarding` asks with a hidden
prompt.

- The key is checked against the server, then saved as a **profile** named after the workspace in
  `~/.config/groundcontrol/config.json` (also on macOS). The directory is 0700 and the file 0600.
- Several workspaces are several profiles: `gc profiles`, `gc profiles use <name>`, `--profile <name>`.
- Key precedence: `--token` > `GC_API_KEY` > profile (`--profile` > `GC_PROFILE` > current).
  URL: `--api-url` > `GC_API_URL` > profile > production.
- `GC_CONFIG_DIR` overrides the config directory (else `$XDG_CONFIG_HOME/groundcontrol`).
- `gc` never prints a key; it shows `gc_live_…abcd` at most.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | ok |
| `1` | API or other error (`GROUNDCONTROL API error <status>: <message>`) |
| `2` | usage error: unknown command or flag, missing argument |
| `3` | not connected: no key, or the server rejected it (401). Prints how to connect. |

## Commands

`gc help` lists them, and `gc help <command>` (or `gc <command> --help`) shows flags and examples.

| | |
| --- | --- |
| Start | `onboarding`, `context`, `changes`, `listen`, `guide` |
| Work | `tasks list\|get\|create\|update`, `comment`, `attach`, `docs …`, `search`, `semantic-search`, `initiatives …`, `members`, `goals …`, `tables …` (datasheets), `journal …` |
| Setup | `profiles`, `skills list\|install`, `version` |

Global flags work anywhere on the line: `--json`, `--field <path>`, `--profile`, `--token`, `--api-url`, and
`--session-id` (`GC_SESSION_ID`, which shows the "working" badge on the task). Markdown bodies come through
stdin:

```sh
gc tasks get <task-id> --since 2h
gc comment <task-id> --body-file - < result.md
gc tasks update <task-id> --status review
```

## Polling and listening

GROUNDCONTROL's `/changes` feed is cursor-based, and **reading never consumes anything**:

- `gc changes --since 2h` reads a window.
- `gc changes --cursor-file <path>` keeps a cursor that belongs to that one poller. The cursor written back
  is the server's (`meta.cursor`), never the local clock, and only after the output was printed.
- `gc listen` is the long-running listener, the successor of the OpenClaw plugin's `gc-worker.sh`. It keeps
  **one cursor per workspace** that only the listener uses, under `<config dir>/listen/`.

```sh
gc listen --all-profiles --exec 'claude -p "Run gc changes --since $GC_LISTEN_SINCE and handle it"'
gc listen --once --all-profiles --exec "openclaw cron run <id>"
```

`--exec` runs once per batch via `sh -c` and waits for it. It receives:

- `GC_PROFILE`, `GC_API_KEY`, `GC_API_URL`: bound to that workspace.
- `GC_LISTEN_SINCE`: the old cursor, so the session sees the same items.
- `GC_LISTEN_COUNT` and `GC_LISTEN_CHANGES_FILE` (the `/changes` response as JSON).

The cursor advances once the command has started, even if it exits non-zero. A failing poll backs off up to
5 minutes, and a request times out after 30 seconds.

- **Stopping.** The command runs in its own process group. The first SIGINT/SIGTERM sends SIGTERM to the whole
  group and cancels a poll in flight, then `gc listen` exits once the command has ended. A second signal sends
  SIGKILL and exits with 130.
- **Rejected keys.** A rejected key on one profile is logged and the others continue. If every profile's key
  is rejected, `gc listen` exits 3.
- **Corrupt cursors.** A cursor file that can't be read is never silently reset to "now". That workspace is
  skipped with an error naming the file, and `--once` exits 1. `--since` resets it.
- **Missing URL.** With `--all-profiles`, a profile without `api_url` uses `GC_API_URL`, then production.

## Skills

| Skill | |
| --- | --- |
| `groundcontrol` | Foundation: setup check, the rules that matter (finish with `review` in scrum workspaces, `backlog` = parked, `for: principal` = tell your human), where to read more |
| `groundcontrol-datasheets` | Datasheets (user-defined tables): rows keyed by field id, select values are option ids |

Install them with `gc skills install` (`--agent claude|codex|openclaw|all`, `--dir`, `--force` after an update).
They are compiled into the binary, so the installed skills always match the `gc` that installed them.
`npx skills add MakersLab-ai/groundcontrol-toolkit -g` works too.

The skills are a deliberately thin bootstrap. **The know-how is served by GROUNDCONTROL itself**: `gc guide`
lists the topics (`start`, `tasks`, `docs`, `datasheets`, `goals`, `coding`), and the guide works without a
key. When agent behaviour changes, that change ships with a server deploy, not with a skill or binary update.

## Development

Go 1.26, standard library plus `golang.org/x/term` (the hidden key prompt).

```sh
go run ./cmd/gc context        # run from source
go test ./...                  # unit tests + the built binary against a mock API (e2e, listen)
go vet ./...
sh scripts/validate-skills.sh  # skill frontmatter
go build -o gc ./cmd/gc
```

| Path | |
| --- | --- |
| `cmd/gc` | `main` (stdio, TTY prompt, signals) and the end-to-end tests |
| `internal/cli` | command table, argument parser, help, every command |
| `internal/api` | HTTP client for `/api/v1` |
| `internal/config` | profiles, `config.json`, key/URL precedence |
| `internal/output` | text / `--json` / `--field` output and formatters |
| `internal/js` | ordered JSON and the JavaScript-style value rules the output formats were defined with |
| `skills.go` | embeds `skills/*/SKILL.md` into the binary |

- The contract with GROUNDCONTROL is its HTTP API (`public/openapi.yaml` in the groundcontrol repo) and the
  agent guide. There is no shared client code to keep in sync.
- `internal/cli/snippets_test.go` checks that every `gc …` snippet in the skills and in this README names an
  existing command and only flags that command knows.

## Releasing

Push a tag `v<version>` on `main`:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

`release.yml` vets and tests, then runs [goreleaser](https://goreleaser.com) (`.goreleaser.yaml`). It builds
darwin/linux × amd64/arm64 with `CGO_ENABLED=0` and the version from the tag, packs `gc_<os>_<arch>.tar.gz`, and
writes `checksums.txt`. It then creates the GitHub release with `install.sh` attached. Go's linker ad-hoc signs
darwin/arm64 binaries itself, so there is no separate signing step. A tag with a suffix (`v0.3.0-rc.1`) is
published as a pre-release, so it never becomes `releases/latest`. CI runs `goreleaser check` and a snapshot
build on every PR. To try it locally:

```sh
goreleaser check
goreleaser release --snapshot --clean     # dist/
```

`GC_INSTALL_BASE` is a testing hook. It points `install.sh` at a directory URL holding the assets, and it moves
the checksum source along with them, so use it only with assets you built yourself:

```sh
(cd dist && python3 -m http.server 8000) &
GC_INSTALL_BASE=http://127.0.0.1:8000 GC_INSTALL_DIR=/tmp/gc-bin sh install.sh
```
