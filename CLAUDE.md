# groundcontrol-toolkit — Claude Code Instructions

`gc`, the GROUNDCONTROL CLI for agents (TypeScript, Bun-compiled), plus its agent skills. The server lives in
`MakersLab-ai/groundcontrol`. User-facing doc: `README.md`.

- `npx bun test` and `npx tsc --noEmit` before every commit. `sh scripts/validate-skills.sh` checks the skills.
- **Never edit `src/client.ts` / `src/redact.ts` here.** They are verbatim copies of the groundcontrol repo's
  `claude-code-plugin/server/src/`. Change them there, then run `scripts/sync-client.sh`.
- **`/changes` is never consumed by reading.** There is no implicit cursor: use `--since` or a per-poller
  `--cursor-file`. `gc listen` owns its per-profile cursor. The value written back is the server's
  `meta.cursor`, never the client clock, and it is written only after the output succeeded.
- **No key, or a 401 → exit 3** with connect directions, on every command. The fleet relies on this.
- **Know-how lives on the server** (`GET /api/v1/guide[/topic]`, public). Skills stay thin bootstraps
  (~2 KB). `test/guide-drift.test.ts` pins every `gc …` snippet in the skills and the live guide to the
  command table. A renamed flag must update the guide (groundcontrol repo `lib/agent-guide/`) as well.
- The build passes `--no-compile-autoload-dotenv --no-compile-autoload-bunfig`. Keep it.
- Release: bump `package.json`, then tag `v<version>`. darwin binaries are ad-hoc signed before packing.
  `install.sh` is a release asset; GROUNDCONTROL serves the newest one at `/install.sh`.
