---
name: groundcontrol-datasheets
description: GROUNDCONTROL datasheets (user-defined tables, API path /tables) through the `gc tables …` commands. Use when a task or the user mentions a datasheet or table in GROUNDCONTROL, before creating, reading or writing datasheet rows or columns.
---

# GROUNDCONTROL Datasheets via `gc tables`

Datasheets are user-defined tables (speaker lists, budgets, inventories) that
humans edit in a grid and you read and write with `gc tables …`. The commands
and ids say `table`; to a human it is always a **datasheet**.

Needs a working `gc` (check with `gc context`; see the `groundcontrol` skill).

## The three rules

1. **404 = no module.** Any `gc tables` call answering HTTP 404 means the datasheet
   does not exist *or* this workspace has no datasheets module. Say so; don't retry.
2. **Rows are keyed by field id** (`fld_…`), never by column name. Read the schema
   first: `gc tables get <table-id>` lists every field id and type.
3. **Select values are option ids** (`opt_…`), never labels — when writing *and*
   when filtering. `gc tables get` lists them next to their labels.

## Workflow

```sh
gc tables list                                   # find the datasheet
gc tables get <table-id>                         # field ids, types, option ids
gc tables rows <table-id> --all                  # every row (labels shown)
gc tables rows <table-id> --filter '[{"field":"fld_ab12","op":"eq","value":"opt_cd34"}]'
gc tables add-rows <table-id> --data-file - <<'EOF'
[{"fld_ab12": "Acme", "fld_cd34": "opt_ef56"}]
EOF
gc tables update-rows <table-id> --data '[{"id":"<row-id>","data":{"fld_cd34":"opt_gh78"}}]'
gc tables comment <table-id> "Imported 40 leads from the CSV."
```

Writes take ≤100 rows and are all-or-nothing. Field types are immutable once
created. After a batch of changes, leave a comment that says what and why.

## Details

Filter operators, field types, paging and examples:

```sh
gc guide datasheets
```
