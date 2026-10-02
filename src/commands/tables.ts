import { bodyInput, compact, jsonInput, list, num, parseJson, requireStr, str, type CommandDef } from '../command'
import { CliError, UsageError } from '../errors'
import { fmtTime, oneLine, pageFooter } from '../output'

// People call them DATASHEETS; the wire (route, ids, errors) says "table".
// A 404 on any of these means "no such datasheet" OR "no datasheets module in
// this workspace" — deliberately the same answer.
const NOT_FOUND_HINT = 'HTTP 404 = no such datasheet, or this workspace has no datasheets module. Don\'t retry.'

const FIELD_TYPES = 'text | number | boolean | date | single_select | multi_select'

const tableLine = (t: any) =>
  [t.id, t.name, `${t.row_count ?? '?'} rows`, `${(t.fields ?? []).length} fields`, t.initiative?.name ? `#${t.initiative.name}` : 'personal'].join('  ')

function fieldLines(fields: any[]): string[] {
  return fields.map((f) => {
    const opts = (f.options ?? []).map((o: any) => `${o.id}=${o.label}`).join(', ')
    return `- ${f.id}  ${f.name}  (${f.type}${f.required ? ', required' : ''})${opts ? `  options: ${opts}` : ''}`
  })
}

/** Row values rendered with field NAMES and option LABELS (ids stay in `gc tables get`). */
export function rowLine(row: any, fields: any[]): string {
  const byId = new Map(fields.map((f) => [f.id, f]))
  const label = (f: any, v: unknown): string => {
    const opts = new Map((f?.options ?? []).map((o: any) => [o.id, o.label]))
    if (Array.isArray(v)) return v.map((x) => String(opts.get(x) ?? x)).join(', ')
    if (typeof v === 'string' && opts.has(v)) return String(opts.get(v))
    return typeof v === 'string' ? oneLine(v, 80) : JSON.stringify(v)
  }
  const cells = Object.entries(row.data ?? {})
    .filter(([, v]) => v !== null && v !== undefined && v !== '')
    .map(([k, v]) => `${byId.get(k)?.name ?? k}=${label(byId.get(k), v)}`)
  return `${row.id}  ${cells.join(' · ') || '(empty)'}`
}

async function withHint<T>(p: Promise<T>): Promise<T> {
  try {
    return await p
  } catch (e) {
    const msg = (e as Error).message
    if (/ 404:/.test(msg)) throw new CliError(msg, NOT_FOUND_HINT)
    throw e
  }
}

export const tableCommands: CommandDef[] = [
  {
    path: ['tables', 'list'],
    summary: 'List datasheets you can see',
    options: {
      initiative: { type: 'string', value: '<id>', desc: 'Only this initiative' },
      q: { type: 'string', value: '<text>', desc: 'Name contains' },
      limit: { type: 'string', value: '<n>', desc: 'Max rows (default 100)' },
      offset: { type: 'string', value: '<n>', desc: 'Skip rows' },
    },
    examples: ['gc tables list'],
    async run(ctx) {
      const v = ctx.values
      const res = await withHint(ctx.client().listTables(compact({ initiative_id: str(v, 'initiative'), q: str(v, 'q'), limit: str(v, 'limit'), offset: str(v, 'offset') })))
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        if (!rows.length) return 'No datasheets.'
        const footer = pageFooter(rows.length, res.meta, 'Next page: --offset {next}')
        return [...rows.map(tableLine), ...(footer ? [footer] : [])]
      })
    },
  },
  {
    path: ['tables', 'get'],
    summary: 'A datasheet\'s schema: field ids (fld_…), types, option ids (opt_…) — read it before writing rows',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc tables get <table-id>'],
    async run(ctx) {
      const res = await withHint(ctx.client().getTable(ctx.args[0]))
      ctx.out.emit(res, () => {
        const t = res.data
        return [
          `# ${t.name}`,
          tableLine(t),
          ...(t.description ? ['', t.description] : []),
          '',
          'Fields:',
          ...fieldLines(t.fields ?? []),
        ]
      })
    },
  },
  {
    path: ['tables', 'create'],
    summary: 'Create a datasheet with columns',
    options: {
      name: { type: 'string', value: '<text>', desc: 'Name (required)' },
      description: { type: 'string', value: '<text>', desc: 'Description' },
      initiative: { type: 'string', value: '<id>', desc: 'Initiative (visibility follows it)' },
      field: { type: 'string', multiple: true, value: '<"Name:type[:opt1,opt2][:required]">', desc: `Column (repeatable); type: ${FIELD_TYPES}` },
      'fields-json': { type: 'string', value: '<json>', desc: 'Columns as JSON: [{name, type, options?, required?}]' },
    },
    examples: [
      'gc tables create --name Leads --field "Company:text:required" --field "Stage:single_select:New,Won,Lost"',
    ],
    async run(ctx) {
      const v = ctx.values
      const specs = list(v, 'field').map(parseFieldSpec)
      const json = str(v, 'fields-json')
      if (json && specs.length) throw new UsageError('Use either --field or --fields-json, not both.')
      const fields = json ? parseJson(json, '--fields-json') : specs.length ? specs : undefined
      const res = await withHint(ctx.client().createTable(compact({ name: requireStr(v, 'name'), description: str(v, 'description'), initiative_id: str(v, 'initiative'), fields })))
      ctx.out.emit(res, () => [`created  ${tableLine(res.data)}`, ...fieldLines(res.data?.fields ?? [])])
    },
  },
  {
    path: ['tables', 'update'],
    summary: 'Rename, re-describe or move a datasheet (creator/owner/admin)',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      name: { type: 'string', value: '<text>', desc: 'New name' },
      description: { type: 'string', value: '<text>', desc: 'New description' },
      initiative: { type: 'string', value: '<id>', desc: 'Move to this initiative' },
    },
    examples: ['gc tables update <table-id> --name "Leads 2026"'],
    async run(ctx) {
      const v = ctx.values
      const patch = compact({ name: str(v, 'name'), description: str(v, 'description'), initiative_id: str(v, 'initiative') })
      if (!Object.keys(patch).length) throw new UsageError('Nothing to update — pass --name, --description or --initiative.')
      const res = await withHint(ctx.client().updateTable(ctx.args[0], patch))
      ctx.out.emit(res, () => `updated  ${tableLine(res.data)}`)
    },
  },
  {
    path: ['tables', 'delete'],
    summary: 'Delete a datasheet with all rows and comments — irreversible (needs --yes)',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    options: { yes: { type: 'boolean', desc: 'Confirm the irreversible delete' } },
    examples: ['gc tables delete <table-id> --yes'],
    async run(ctx) {
      if (ctx.values.yes !== true) throw new UsageError('Deleting a datasheet is irreversible — add --yes to confirm.')
      const res = await withHint(ctx.client().deleteTable(ctx.args[0]))
      ctx.out.emit(res ?? { deleted: ctx.args[0] }, () => `deleted datasheet ${ctx.args[0]}`)
    },
  },
  {
    path: ['tables', 'rows'],
    summary: 'Read rows (one page, or every match with --all)',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      filter: { type: 'string', value: '<json>', desc: '[{"field":"fld_…","op":"eq","value":"opt_…"}], AND-combined' },
      sort: { type: 'string', value: '<fld_x:asc|desc>', desc: 'Sort by a field' },
      q: { type: 'string', value: '<text>', desc: 'Full-text match' },
      all: { type: 'boolean', desc: 'Walk every page (complete result)' },
      limit: { type: 'string', value: '<n>', desc: 'Page size (server max 200)' },
      offset: { type: 'string', value: '<n>', desc: 'Skip rows' },
    },
    details: 'Filter select fields by option id (opt_…), never by label. Operators: gc guide datasheets',
    examples: ['gc tables rows <table-id> --all', 'gc tables rows <table-id> --filter \'[{"field":"fld_ab12","op":"eq","value":"opt_cd34"}]\''],
    async run(ctx) {
      const v = ctx.values
      const filterRaw = str(v, 'filter')
      const filter = filterRaw ? JSON.stringify(parseJson(filterRaw, '--filter')) : undefined
      const params = compact({ filter, sort: str(v, 'sort'), q: str(v, 'q'), limit: str(v, 'limit'), offset: str(v, 'offset') }) as Record<string, string>
      const client = ctx.client()
      const id = ctx.args[0]
      const res = v.all === true
        ? await withHint(client.listAllRows(id, compact({ filter, sort: str(v, 'sort'), q: str(v, 'q') }) as Record<string, string>))
        : await withHint(client.listRows(id, params))
      if (ctx.out.raw) return ctx.out.emit(res, () => '')
      const table = await withHint(client.getTable(id))
      const fields: any[] = table?.data?.fields ?? []
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        const lines = rows.length ? rows.map((r) => rowLine(r, fields)) : ['No rows.']
        if (res?.meta?.truncated) lines.push(`-- truncated after ${rows.length} of ${res.meta.matched} matching rows`)
        else if (v.all !== true) {
          const footer = pageFooter(rows.length, res.meta, 'Next page: --offset {next}, or --all')
          if (footer) lines.push(footer)
        }
        return lines
      })
    },
  },
  {
    path: ['tables', 'add-rows'],
    summary: 'Add ≤100 rows (all or nothing); each row keyed by field id, select values are option ids',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      data: { type: 'string', value: '<json>', desc: '[{"fld_…": value, …}, …]' },
      'data-file': { type: 'string', value: '<path|->', desc: 'Same JSON from a file, or stdin with -' },
    },
    examples: ['gc tables add-rows <table-id> --data \'[{"fld_ab12":"Acme","fld_cd34":"opt_ef56"}]\''],
    async run(ctx) {
      const rows = await jsonInput(ctx)
      if (!Array.isArray(rows) || rows.length === 0) throw new UsageError('Rows must be a non-empty JSON array of objects keyed by field id.')
      const res = await withHint(ctx.client().createRows(ctx.args[0], rows))
      ctx.out.emit(res, () => `added ${(res?.data ?? rows).length} row(s)`)
    },
  },
  {
    path: ['tables', 'update-rows'],
    summary: 'Change ≤100 rows (all or nothing): [{id, data}], changed fields only, null clears',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      data: { type: 'string', value: '<json>', desc: '[{"id":"<row-id>","data":{"fld_…": value}}]' },
      'data-file': { type: 'string', value: '<path|->', desc: 'Same JSON from a file, or stdin with -' },
    },
    examples: ['gc tables update-rows <table-id> --data \'[{"id":"<row-id>","data":{"fld_cd34":"opt_gh78"}}]\''],
    async run(ctx) {
      const rows = await jsonInput(ctx)
      if (!Array.isArray(rows) || rows.length === 0) throw new UsageError('Expected a non-empty JSON array of {id, data}.')
      const res = await withHint(ctx.client().updateRows(ctx.args[0], rows as { id: string; data: unknown }[]))
      ctx.out.emit(res, () => `updated ${(res?.data ?? rows).length} row(s)`)
    },
  },
  {
    path: ['tables', 'delete-rows'],
    summary: 'Delete ≤100 rows by id (unknown ids are skipped)',
    args: '<table-id> <row-id>…',
    minArgs: 2,
    maxArgs: 101,
    examples: ['gc tables delete-rows <table-id> <row-id> <row-id>'],
    async run(ctx) {
      const [id, ...ids] = ctx.args
      const res = await withHint(ctx.client().deleteRows(id, ids))
      ctx.out.emit(res ?? { deleted: ids }, () => `deleted ${res?.data?.deleted ?? ids.length} row(s)`)
    },
  },
  {
    path: ['tables', 'field-add'],
    summary: 'Append a column',
    args: '<table-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      name: { type: 'string', value: '<text>', desc: 'Column name (required)' },
      type: { type: 'string', value: '<type>', desc: `${FIELD_TYPES} (required, immutable)` },
      option: { type: 'string', multiple: true, value: '<label>', desc: 'Select option label (repeatable)' },
      required: { type: 'boolean', desc: 'Mark the column required' },
    },
    examples: ['gc tables field-add <table-id> --name Stage --type single_select --option New --option Won'],
    async run(ctx) {
      const v = ctx.values
      const opts = list(v, 'option')
      const res = await withHint(ctx.client().addField(ctx.args[0], compact({
        name: requireStr(v, 'name'), type: requireStr(v, 'type'), options: opts.length ? opts : undefined, required: v.required === true ? true : undefined,
      })))
      ctx.out.emit(res, () => fieldLines((res?.data?.fields ?? [res?.data]).filter(Boolean)).join('\n') || 'field added')
    },
  },
  {
    path: ['tables', 'field-update'],
    summary: 'Edit a column; --options-json is the COMPLETE new option list ({id,label} keeps, no id adds)',
    args: '<table-id> <field-id>',
    minArgs: 2,
    maxArgs: 2,
    options: {
      name: { type: 'string', value: '<text>', desc: 'New name' },
      required: { type: 'string', value: '<true|false>', desc: 'Required flag' },
      position: { type: 'string', value: '<n>', desc: 'Column position' },
      'options-json': { type: 'string', value: '<json>', desc: '[{"id":"opt_…","label":"…"},{"label":"New"}]' },
    },
    examples: ['gc tables field-update <table-id> fld_ab12 --name "Company name"'],
    async run(ctx) {
      const v = ctx.values
      const req = str(v, 'required')
      if (req !== undefined && req !== 'true' && req !== 'false') throw new UsageError('--required must be true or false.')
      const optionsRaw = str(v, 'options-json')
      const patch = compact({
        name: str(v, 'name'),
        required: req === undefined ? undefined : req === 'true',
        position: num(str(v, 'position'), 'position') ?? undefined,
        options: optionsRaw ? parseJson(optionsRaw, '--options-json') : undefined,
      })
      if (!Object.keys(patch).length) throw new UsageError('Nothing to update.')
      const res = await withHint(ctx.client().updateField(ctx.args[0], ctx.args[1], patch))
      ctx.out.emit(res, () => `updated field ${ctx.args[1]}`)
    },
  },
  {
    path: ['tables', 'field-delete'],
    summary: 'Remove a column',
    args: '<table-id> <field-id>',
    minArgs: 2,
    maxArgs: 2,
    examples: ['gc tables field-delete <table-id> fld_ab12'],
    async run(ctx) {
      const res = await withHint(ctx.client().deleteField(ctx.args[0], ctx.args[1]))
      ctx.out.emit(res ?? { deleted: ctx.args[1] }, () => `deleted field ${ctx.args[1]}`)
    },
  },
  {
    path: ['tables', 'comment'],
    summary: 'Comment on a datasheet: say what you changed and why',
    args: '<table-id> [<body> | -]',
    minArgs: 1,
    maxArgs: 2,
    options: { 'body-file': { type: 'string', value: '<path|->', desc: 'Read the body from a file, or stdin with -' } },
    examples: ['gc tables comment <table-id> "Imported 40 leads from the CSV."'],
    async run(ctx) {
      const res = await withHint(ctx.client().createTableComment(ctx.args[0], await bodyInput(ctx, ctx.args[1])))
      ctx.out.emit(res, () => `commented on datasheet ${ctx.args[0]} · ${fmtTime(res?.data?.created_at)}`)
    },
  },
]

/** `"Name:type[:opt1,opt2][:required]"` → field input. */
export function parseFieldSpec(spec: string): { name: string; type: string; options?: string[]; required?: boolean } {
  const parts = spec.split(':')
  const [name, type] = [parts[0]?.trim(), parts[1]?.trim()]
  if (!name || !type) throw new UsageError(`--field "${spec}": expected "Name:type[:opt1,opt2][:required]".`)
  let required: boolean | undefined
  let options: string[] | undefined
  for (const p of parts.slice(2)) {
    if (p.trim() === 'required') required = true
    else if (p.trim()) options = p.split(',').map((s) => s.trim()).filter(Boolean)
  }
  return compact({ name, type, options, required }) as { name: string; type: string }
}
