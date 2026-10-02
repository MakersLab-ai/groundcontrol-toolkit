import { bodyInput, compact, nullable, num, requireStr, str, textInput, type CommandDef } from '../command'
import { UsageError } from '../errors'
import { apiGet } from '../http'
import { commentBlock, fmtTime, name, oneLine, pad, pageFooter } from '../output'

const docLine = (d: any) =>
  [d.id, fmtTime(d.updated_at), d.title, d.initiative?.name ? `#${d.initiative.name}` : 'personal', d.archived_at ? '(archived)' : '']
    .filter(Boolean)
    .join('  ')

const initiativeLine = (i: any) =>
  [
    i.id,
    pad(i.visibility, 7),
    i.name,
    typeof i.task_count === 'number' ? `${i.task_count} tasks, ${i.doc_count ?? 0} docs` : '',
    i.content_visible === false ? '(contents not visible to you)' : '',
    i.archived_at ? '(archived)' : '',
  ]
    .filter(Boolean)
    .join('  ')

export const docCommands: CommandDef[] = [
  {
    path: ['docs', 'list'],
    summary: 'List documents',
    options: {
      initiative: { type: 'string', value: '<id>', desc: 'Only this initiative' },
      q: { type: 'string', value: '<text>', desc: 'Title/content contains' },
      archived: { type: 'boolean', desc: 'Include archived docs' },
      limit: { type: 'string', value: '<n>', desc: 'Max rows (default 50)' },
      offset: { type: 'string', value: '<n>', desc: 'Skip rows (paging)' },
    },
    examples: ['gc docs list', 'gc docs list --initiative <initiative-id> --q roadmap'],
    async run(ctx) {
      const v = ctx.values
      const res = await ctx.client().listDocs(compact({
        initiative_id: str(v, 'initiative'), q: str(v, 'q'), archived: v.archived === true ? 'true' : undefined,
        limit: str(v, 'limit') ?? '50', offset: str(v, 'offset'),
      }) as Record<string, string>)
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        if (!rows.length) return 'No docs.'
        const footer = pageFooter(rows.length, res.meta, 'Next page: --offset {next}')
        return [...rows.map(docLine), ...(footer ? [footer] : [])]
      })
    },
  },
  {
    path: ['docs', 'get'],
    summary: 'Show a document (Markdown content)',
    args: '<doc-id>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc docs get <doc-id>', 'gc docs get <doc-id> --field content > doc.md'],
    async run(ctx) {
      const res = await ctx.client().getDoc(ctx.args[0])
      ctx.out.emit(res, () => {
        const d = res.data
        const by = name(d.updated_by) ?? name(d.created_by)
        const lines = [
          `# ${d.title}`,
          `${d.id} · ${d.initiative?.name ? `#${d.initiative.name}` : 'personal'} · updated ${fmtTime(d.updated_at)}${by ? ` by ${by}` : ''}${d.archived_at ? ' · archived' : ''}`,
          '',
          String(d.content ?? '').trimEnd(),
        ]
        const tasks: any[] = d.linked_tasks ?? d.tasks ?? []
        if (tasks.length) lines.push('', `## Linked tasks (${tasks.length})`, ...tasks.map((t) => `- ${t.id}  ${t.status ?? ''}  ${t.title}`))
        const comments: any[] = d.comments ?? []
        if (comments.length) lines.push('', `## Comments (${comments.length})`, ...comments.map(commentBlock))
        return lines
      })
    },
  },
  {
    path: ['docs', 'create'],
    summary: 'Create a Markdown document (no initiative = personal)',
    options: {
      title: { type: 'string', value: '<text>', desc: 'Title (required)' },
      content: { type: 'string', value: '<md>', desc: 'Markdown content' },
      'content-file': { type: 'string', value: '<path|->', desc: 'Read the content from a file, or stdin with -' },
      initiative: { type: 'string', value: '<id>', desc: 'Initiative id' },
      slug: { type: 'string', value: '<slug>', desc: 'URL slug (default: from the title)' },
    },
    examples: ['gc docs create --title "Release notes" --content-file notes.md --initiative <initiative-id>'],
    async run(ctx) {
      const v = ctx.values
      const content = await textInput(ctx, 'content', 'content-file')
      if (content === undefined) throw new UsageError('Missing --content or --content-file.')
      const res = await ctx.client().createDoc(compact({ title: requireStr(v, 'title'), content, initiative_id: str(v, 'initiative'), slug: str(v, 'slug') }))
      ctx.out.emit(res, () => `created  ${docLine(res.data)}`)
    },
  },
  {
    path: ['docs', 'update'],
    summary: 'Update a document (only what you pass changes)',
    args: '<doc-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      title: { type: 'string', value: '<text>', desc: 'New title' },
      content: { type: 'string', value: '<md>', desc: 'New Markdown content (replaces it)' },
      'content-file': { type: 'string', value: '<path|->', desc: 'Read the new content from a file, or stdin with -' },
      initiative: { type: 'string', value: '<id|none>', desc: 'Move to an initiative ("none" = personal)' },
    },
    examples: ['gc docs update <doc-id> --content-file doc.md'],
    async run(ctx) {
      const v = ctx.values
      const patch = compact({ title: str(v, 'title'), content: await textInput(ctx, 'content', 'content-file'), initiative_id: nullable(str(v, 'initiative')) })
      if (!Object.keys(patch).length) throw new UsageError('Nothing to update — pass --title, --content(-file) or --initiative.')
      const res = await ctx.client().updateDoc(ctx.args[0], patch)
      ctx.out.emit(res, () => `updated  ${docLine(res.data)}`)
    },
  },
  {
    path: ['docs', 'archive'],
    summary: 'Archive (soft-delete) a document',
    args: '<doc-id>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc docs archive <doc-id>'],
    async run(ctx) {
      const res = await ctx.client().archiveDoc(ctx.args[0])
      ctx.out.emit(res ?? { archived: ctx.args[0] }, () => `archived ${ctx.args[0]}`)
    },
  },
  {
    path: ['docs', 'comment'],
    summary: 'Post a Markdown comment on a document',
    args: '<doc-id> [<body> | -]',
    minArgs: 1,
    maxArgs: 2,
    options: { 'body-file': { type: 'string', value: '<path|->', desc: 'Read the body from a file, or stdin with -' } },
    examples: ['gc docs comment <doc-id> "Updated section 2."'],
    async run(ctx) {
      const res = await ctx.client().createDocComment(ctx.args[0], await bodyInput(ctx, ctx.args[1]))
      ctx.out.emit(res, () => `commented on doc ${ctx.args[0]} (${res?.data?.id ?? 'ok'})`)
    },
  },
]

export const initiativeCommands: CommandDef[] = [
  {
    path: ['initiatives', 'list'],
    summary: 'List initiatives (project containers) you can see',
    options: {
      counts: { type: 'boolean', desc: 'Include task/doc counts' },
      limit: { type: 'string', value: '<n>', desc: 'Max rows (default 100)' },
      offset: { type: 'string', value: '<n>', desc: 'Skip rows (paging)' },
    },
    examples: ['gc initiatives list', 'gc initiatives list --counts'],
    async run(ctx) {
      // The shared client's listInitiatives() takes no params (route default: 50
      // rows), so paging and the opt-in ?with_counts=true go through apiGet.
      const qs = new URLSearchParams(compact({
        limit: str(ctx.values, 'limit') ?? '100', offset: str(ctx.values, 'offset'), with_counts: ctx.values.counts === true ? 'true' : undefined,
      }) as Record<string, string>)
      const res = await apiGet(ctx, `/initiatives?${qs}`)
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        if (!rows.length) return 'No initiatives.'
        const footer = pageFooter(rows.length, res.meta, 'Next page: --offset {next}')
        return [...rows.map(initiativeLine), ...(footer ? [footer] : [])]
      })
    },
  },
  {
    path: ['initiatives', 'get'],
    summary: 'Show an initiative with its counts and summary',
    args: '<initiative-id>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc initiatives get <initiative-id>'],
    async run(ctx) {
      const res = await ctx.client().getInitiative(ctx.args[0])
      ctx.out.emit(res, () => {
        const i = res.data
        return [
          `# ${i.name}`,
          initiativeLine(i),
          i.description ? `\n${i.description}` : '',
          i.summary ? `\nSummary: ${i.summary}` : '',
          i.memory_summary ? `\nMemory: ${i.memory_summary}` : '',
        ].filter(Boolean)
      })
    },
  },
  {
    path: ['initiatives', 'memory'],
    summary: "An initiative's agent memory (insights, key decisions, stats)",
    args: '<initiative-id>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc initiatives memory <initiative-id> --json'],
    async run(ctx) {
      const res = await ctx.client().getInitiativeMemory(ctx.args[0])
      ctx.out.emit(res, () => JSON.stringify(res?.data ?? res, null, 2))
    },
  },
]

export const searchCommands: CommandDef[] = [
  {
    path: ['search'],
    summary: 'Full-text search across tasks, docs and comments',
    args: '<query>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      type: { type: 'string', value: '<t>', desc: 'tasks | docs | comments | all (default)' },
      initiative: { type: 'string', value: '<id>', desc: 'Only this initiative' },
      limit: { type: 'string', value: '<n>', desc: 'Max results per type (default 20)' },
    },
    examples: ['gc search "login redirect"', 'gc search invoice --type docs'],
    async run(ctx) {
      const v = ctx.values
      const res = await ctx.client().search(compact({ q: ctx.args[0], type: str(v, 'type'), initiative_id: str(v, 'initiative'), limit: str(v, 'limit') }))
      ctx.out.emit(res, () => {
        const d = res?.data ?? {}
        const lines: string[] = []
        for (const t of d.tasks ?? []) lines.push(`task     ${t.id}  ${pad(t.status, 11)} ${t.title}`)
        for (const x of d.docs ?? []) lines.push(`doc      ${x.id}  ${x.title}`)
        for (const c of d.comments ?? []) lines.push(`comment  ${c.source_type === 'doc_comment' ? 'doc' : 'task'} ${c.source_id} "${c.source_title}" · ${name(c.author) ?? '?'}: ${oneLine(c.body, 120)}`)
        return lines.length ? lines : 'No results.'
      })
    },
  },
  {
    path: ['semantic-search'],
    summary: 'Find tasks, docs and comments by meaning (needs OpenAI on the server)',
    args: '<query>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      types: { type: 'string', value: '<a,b>', desc: 'task, doc, task_comment, doc_comment, journal' },
      limit: { type: 'string', value: '<n>', desc: '1–50 (default 10)' },
    },
    examples: ['gc semantic-search "why did we drop sub-tasks"'],
    async run(ctx) {
      const types = str(ctx.values, 'types')?.split(',').map((s) => s.trim()).filter(Boolean)
      const limit = num(str(ctx.values, 'limit'), 'limit') ?? undefined
      const res = await ctx.client().semanticSearch(compact({ query: ctx.args[0], types, limit }) as { query: string })
      ctx.out.emit(res, () => {
        const hits: any[] = res?.data?.hits ?? []
        if (!hits.length) return res?.data?.note ?? 'No results.'
        return hits.map((h) => `${pad(h.entity_type, 12)} ${h.entity_id}  ${typeof h.similarity === 'number' ? h.similarity.toFixed(2) : ''}  ${h.title ?? ''} ${h.snippet ? '— ' + oneLine(h.snippet, 120) : ''}`.trimEnd())
      })
    },
  },
]

export const journalCommands: CommandDef[] = [
  {
    path: ['journal', 'list'],
    summary: 'Recent journal entries (default: last 14 days)',
    options: {
      from: { type: 'string', value: '<YYYY-MM-DD>', desc: 'From date' },
      to: { type: 'string', value: '<YYYY-MM-DD>', desc: 'To date' },
      limit: { type: 'string', value: '<n>', desc: 'Max entries' },
    },
    examples: ['gc journal list --from 2026-09-25'],
    async run(ctx) {
      const v = ctx.values
      const res = await ctx.client().listJournal(compact({ from: str(v, 'from'), to: str(v, 'to'), limit: str(v, 'limit') }))
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        return rows.length ? rows.map((e) => `${e.date}  ${e.activity_snapshot ? `${e.activity_snapshot.tasks_completed} done, ${e.activity_snapshot.comments_added} comments  ` : ''}${oneLine(e.entry?.agent_summary ?? e.entry?.summary ?? '', 140)}`) : 'No journal entries.'
      })
    },
  },
  {
    path: ['journal', 'get'],
    summary: 'Get (or auto-generate) the journal entry for a day',
    args: '<YYYY-MM-DD>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc journal get 2026-10-01'],
    async run(ctx) {
      const res = await ctx.client().getJournalDay(ctx.args[0])
      ctx.out.emit(res, () => JSON.stringify(res?.data ?? res, null, 2))
    },
  },
  {
    path: ['journal', 'summary'],
    summary: "Save your 2–4 sentence summary for a day",
    args: '<YYYY-MM-DD> [<text> | -]',
    minArgs: 1,
    maxArgs: 2,
    options: { 'body-file': { type: 'string', value: '<path|->', desc: 'Read the summary from a file, or stdin with -' } },
    examples: ['gc journal summary 2026-10-01 "Shipped the CLI; reviewed two PRs."'],
    async run(ctx) {
      const res = await ctx.client().saveJournalSummary(ctx.args[0], await bodyInput(ctx, ctx.args[1]))
      ctx.out.emit(res, () => `saved summary for ${ctx.args[0]}`)
    },
  },
]
