# groundcontrol-toolkit — Claude Code Instructions

`gc`, the GROUNDCONTROL CLI for agents (Go), plus its agent skills. The server lives in
`MakersLab-ai/groundcontrol`. User-facing doc: `README.md`.

- Before every commit: `go vet ./...`, `go test ./...`, `sh scripts/validate-skills.sh`.
- **Go conventions:** standard library only; the one dependency is `golang.org/x/term` (hidden key prompt).
  Don't add cobra or any other CLI framework. `cmd/gc/main.go` only wires the process: stdio, TTY, signals,
  version. Everything else lives in `internal/`: `cli` (commands, parser, help), `api`, `config`, `output`,
  and `js` (ordered JSON, JS-style coercions). Commands return errors. `clierr` maps them to exit codes,
  and nothing but `main` calls `os.Exit`. `gofmt` everything.
- **The server is the contract.** The CLI talks to GROUNDCONTROL only over HTTP: `public/openapi.yaml` in the
  groundcontrol repo, and the agent guide (`GET /api/v1/guide[/topic]`). There is **no client copy, no sync
  script and no drift test against the server**. Plugin trees are being retired, so don't port code from them.
- **The command surface is public.** The server-side guide names `gc` commands and flags, so commands, flags,
  help examples, output formats, exit codes and env vars stay stable. A rename must update the guide
  (groundcontrol repo `lib/agent-guide/`) in the same change. `internal/cli/snippets_test.go` pins every
  `gc …` snippet in `skills/*/SKILL.md` and `README.md` to the command table.
- **`/changes` is never consumed by reading.** There is no implicit cursor: use `--since` or a per-poller
  `--cursor-file`. `gc listen` owns its per-profile cursor (`<config dir>/listen/<name>.json`). The value
  written back is the server's `meta.cursor` (fallback `checked_at`), never the client clock. It is written
  only after the output succeeded, and for `--exec` once the command has started.
- **No key, or a 401 → exit 3** with connect directions, on every command. The fleet relies on this. Usage
  errors are 2, everything else 1. Never print an API key (`gc_live_…abcd` at most).
- **Config compatibility:** `config.json` keeps the shape `{current, profiles{api_url, api_key, workspace,
  agent, created_at}}` and its profile order. The directory is 0700, the file 0600, and writes are atomic
  (tmp + rename).
- **Thin skills:** know-how lives on the server. Skills stay bootstraps of about 2 KB (a test caps them below
  3 KB). They are embedded via `skills.go` (`go:embed`), so a skill change ships with the next release.
- **Version pinning:** the version comes only from the git tag (`-X main.version=…` via goreleaser); there is
  no version file. Release = tag `v<version>` on main (`release.yml` → goreleaser). `install.sh` is a release
  asset. Pin GitHub Actions by commit SHA.
