---
name: groundcontrol
description: Work with GROUNDCONTROL (tasks, comments, docs, goals, the human-agent kanban board) through the `gc` command line. Use whenever a task, the user or a message mentions GROUNDCONTROL, a GC task id, the board, or asks you to report progress there — before reading or changing anything in it.
---

# GROUNDCONTROL via `gc`

GROUNDCONTROL is the board your humans and you share. You reach it with the
`gc` CLI (alias `groundcontrol`). Output is compact text; add `--json` for the
raw API response or `--field <path>` for one value.

## Setup check

```sh
gc context
```

It prints who you are, the workspace, its workflow and your open tasks. Exit
code 3 means "not connected" (no key, or the key was rejected): relay the steps
it prints to your human, then run `gc onboarding --token <key>` (or
`printf %s "$KEY" | gc onboarding --token -`). Never print or echo the key.

## Rules

1. **Start with `gc context`.** It tells you the workflow:
   - **scrum** → finish with `gc tasks update <id> --status review`, never `done` (a human sets done).
   - **kanban** → finish with `--status done`.
   - A task in **`backlog`** is parked — never start it; its move to `todo` is the go-signal.
2. **Catch up with `gc changes --since <iso|15m|2h|1d>`.** There is no hidden cursor. A
   long-running poller keeps its own: `gc changes --cursor-file <path>`.
3. **`[principal]` items** concern the human you assist, not you: do NOT start them —
   tell your human about them in the current session.
4. **Comment as you work:** `gc comment <task-id> "…"` when you start, when blocked, and
   a closing comment with the result (links, PRs, files). Longer Markdown via stdin:
   `gc comment <task-id> --body-file - <<'EOF' … EOF`.
5. **Read before you act:** `gc tasks get <id>` shows description, attachments and all
   comments; `--since 2h` only the new ones.
6. Files: `gc attach <task-id> <path>`. Search: `gc search "<text>"`.

## Common commands

```sh
gc tasks list --assigned-to me --status todo,in_progress
gc tasks update <id> --status in_progress
gc tasks create --title "…" --description-file - --assign me
gc docs get <id> · gc goals list --mine · gc initiatives list
```

Every command has `--help` with examples.

## Details

The full, always-current guide lives on the server:

```sh
gc guide            # list topics
gc guide <topic>    # start, tasks, docs, datasheets, goals, coding
```

Read `gc guide start` once per session if you are unsure how to proceed.
