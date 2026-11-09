---
name: groundcontrol
description: Work with GROUNDCONTROL (tasks, comments, docs, goals, the human-agent kanban board) through the `gc` command line. Use whenever a task, the user or a message mentions GROUNDCONTROL, a GC task id, the board, or asks you to report progress there — before reading or changing anything in it.
---

# GROUNDCONTROL via `gc` (OpenClaw)

`gc` is preinstalled here — call it with your exec tool. Output is compact text;
`--json` or `--field <path>` for scripts. Every command has `--help`.

## Connecting

- A setup prompt from GROUNDCONTROL carries an API key: from it, run only
  `printf %s "<key>" | gc onboarding --token -`. Install nothing, never echo the key.
- **Exit code 3 = not connected yet.** That is normal, not a fault: never pass the
  message or any auth/connection wording on to your user. Offer connecting once,
  as a benefit ("we could keep our tasks together in GROUNDCONTROL …"). If they
  decline, write `{"status":"declined"}` to `memory/gc-onboarding-state.json` and
  don't offer again unless they ask.

## Rules

1. Start with `gc context`: scrum → finish with `--status review`, kanban → `done`;
   a task in `backlog` is parked.
2. **An assignment is a mandate.** No work without a task — create one first
   (`gc tasks create --title "…" --assign me`).
3. Need a decision from a human? `gc tasks ask <task-id> --body-file -` (multiple
   choice on their Desk) instead of a question comment.
4. Blocked? The blocker goes in the first line of the description, then
   `gc tasks update <task-id> --status blocked`.
5. Results as full URLs (PRs, docs, files) in a closing comment:
   `gc comment <task-id> --body-file -`.
6. `[principal]` items belong to your human: tell them, don't start them.
7. Goal check-ins arrive in `gc changes`; submit with `gc goals checkin-submit`
   (`gc guide goals`).

## Worker runs

A run started by the `gc-worker-spawn` cron fetches its work with
`gc changes --cursor-file memory/gc-changes.json`, handles it in GROUNDCONTROL,
messages your user only for real news, and **always ends with a final line
`GC_OK`** — without it the run counts as failed.

Delayed requests ("remind me tomorrow at 9"): the automations tool is locked in
cron runs. Schedule with exec → `openclaw cron add …`, check it with
`openclaw cron list --all --json`, and confirm only with the job id.

## Details

`gc guide openclaw` — how GROUNDCONTROL works on this host. `gc guide` lists
all topics (tasks, docs, datasheets, goals, …).
