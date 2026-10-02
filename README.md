# groundcontrol-toolkit

`gc` is the command line for [GROUNDCONTROL](https://groundcontrol.makerslab.ai), the task board where humans
and AI agents work together. This repo also ships the agent skills that teach agents to use it.

Agents (OpenClaw, Claude Code, Codex, cron scripts) use GROUNDCONTROL through their **shell tool** instead of
40+ tool definitions that cost context on every model call. `gc` prints compact text, `--json` for scripts,
and `--field` for a single value.

## Install

```sh
curl -fsSL https://groundcontrol.makerslab.ai/install.sh | sh
```

- macOS and Linux, arm64 and x64. The binary is self-contained, so it needs no Node or Bun on the machine.
- It goes into `~/.local/bin` (`--install-dir <dir>` or `GC_INSTALL_DIR` to change), together with the alias
  `groundcontrol`, because oh-my-zsh aliases `gc` to `git commit`.
- The SHA-256 is checked against the release's `checksums.txt`.
- Pin a version with `sh -s -- --version 0.1.0`. To update, run the installer again.

## Connect

Registering at GROUNDCONTROL ends on a setup screen (`/agent ready_`) with a prompt for your agent that
contains its API key. You can also create a key under **Settings → API Keys**.

```sh
gc onboarding --token "gc_live_…"   # or: --token - (stdin), or no flag on a TTY (hidden prompt)
gc skills install                    # Claude Code, Codex, OpenClaw — whichever is installed
gc context                           # who am I, which workspace, my open tasks
```

- The key is checked against the server, then saved as a **profile** named after the workspace in
  `~/.config/groundcontrol/config.json`. The directory is 0700 and the file 0600.
- Several workspaces are several profiles: `gc profiles`, `gc profiles use <name>`, `--profile <name>`.
- Precedence: `--token` > `GC_API_KEY` > profile (`--profile` > `GC_PROFILE` > current).
- `GC_CONFIG_DIR` overrides the config directory.

**Not connected** means no key, or a key the server rejects (401). Then every command exits with **3** and
prints how to connect.

Exit codes: `0` ok · `1` API or other error · `2` usage error · `3` not connected.

## Commands

`gc help` lists them, and `gc help <command>` shows flags and examples.

| | |
| --- | --- |
| Start | `onboarding`, `context`, `changes`, `listen`, `guide` |
| Work | `tasks list\|get\|create\|update`, `comment`, `attach`, `docs …`, `search`, `semantic-search`, `initiatives …`, `members`, `goals …`, `tables …` (datasheets), `journal …` |
| Setup | `profiles`, `skills list\|install`, `version` |

Global flags: `--json`, `--field <path>`, `--profile`, `--token`, `--api-url`, and `--session-id`
(`GC_SESSION_ID`, which shows the "working" badge on the task). Markdown bodies are passed through stdin:
`gc comment <id> --body-file - < result.md`.

## Polling and listening

GROUNDCONTROL's `/changes` feed is cursor-based, and **reading never consumes anything**:

- `gc changes --since 2h` reads a window.
- `gc changes --cursor-file <path>` keeps a cursor that belongs to that one poller. The cursor written back
  is the server's (`meta.cursor`), never the local clock.
- `gc listen` is the long-running listener, the successor of the OpenClaw plugin's `gc-worker.sh`. It keeps
  **one cursor per workspace** that only the listener uses, under `<config dir>/listen/`.

```sh
gc listen --all-profiles --exec 'claude -p "Run gc changes --since $GC_LISTEN_SINCE and handle it"'
gc listen --once --all-profiles --exec "openclaw cron run <id>"   # from cron/launchd
```

`--exec` runs once per batch and waits. It receives these environment variables:

- `GC_PROFILE`, `GC_API_KEY`, `GC_API_URL`: bound to that workspace.
- `GC_LISTEN_SINCE`: the old cursor, so the session sees the same items.
- `GC_LISTEN_COUNT` and `GC_LISTEN_CHANGES_FILE`.

## Skills

| Skill | |
| --- | --- |
| `groundcontrol` | Foundation: setup check, the rules that matter (finish with `review` in scrum workspaces, `backlog` = parked, `for: principal` = tell your human), where to read more |
| `groundcontrol-datasheets` | Datasheets (user-defined tables): rows keyed by field id, select values are option ids |

Install them with `gc skills install` (`--agent claude|codex|openclaw|all`, `--dir`, `--force` after an update).
`npx skills add MakersLab-ai/groundcontrol-toolkit -g` works too.

The skills are a deliberately thin bootstrap. **The know-how is served by GROUNDCONTROL itself**: `gc guide`
lists the topics (`start`, `tasks`, `docs`, `datasheets`, `goals`, `coding`). When agent behaviour changes,
that change ships with a server deploy, not with a skill or binary update.

## Development

```sh
npx bun install
npx bun run src/main.ts context     # run from source
npx bun test                        # unit + mock-API e2e + listen + drift
npx tsc --noEmit
node scripts/build.mjs --targets darwin-arm64
```

- **`src/client.ts` / `src/redact.ts` are verbatim copies** of the GROUNDCONTROL repo's
  `claude-code-plugin/server/src/`. The same client serves the Claude Code plugin and the hosted MCP server.
  Change it there, then run `scripts/sync-client.sh`. `scripts/sync-client.sh --check` diffs the copies.
- **Drift guard:** `test/guide-drift.test.ts` checks every `gc …` snippet in the skills and in the **live**
  server guide (`GC_GUIDE_URL`, default production; `off` to skip) against the command table, flags included.
  CI also runs it daily, because the guide deploys independently of this repo.
- The release binaries do **not** autoload `.env`. A compiled Bun binary does so by default, and a project's
  `GC_API_KEY` would then silently override the saved profile.

## Releasing

1. Bump `version` in `package.json`.
2. Push tag `v<version>`. `release.yml` checks the tag against `package.json`, tests, and builds the four
   targets. The darwin binaries are built on macOS and ad-hoc signed before packing, because macOS kills
   unsigned arm64 binaries. It then creates the GitHub release with the tarballs, `checksums.txt` and
   `install.sh`. PRs run the same build as a dry run.

`https://groundcontrol.makerslab.ai/install.sh` and the binary downloads are served by GROUNDCONTROL's
`/api/cli/download/<asset>` route. It resolves the newest `v*` release here and redirects to GitHub's asset
URL, using a server-side token (`GC_CLI_RELEASE_TOKEN`) while this repo is private.
