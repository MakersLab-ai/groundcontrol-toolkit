import { basename } from 'node:path'
import { existsSync } from 'node:fs'
import { bodyInput, compact, nullable, num, requireStr, str, textInput, type CommandDef, type Ctx } from '../command'
import { CliError, UsageError } from '../errors'
import { commentBlock, fmtTime, humanSize, name, pageFooter, taskLine } from '../output'
import { parseSince } from '../since'

const STATUSES = 'backlog | scheduled | todo | in_progress | blocked | review | done'

const taskFieldFlags = {
  description: { type: 'string', value: '<md>', desc: 'Markdown description' },
  'description-file': { type: 'string', value: '<path|->', desc: 'Read the description from a file, or stdin with -' },
  status: { type: 'string', value: '<status>', desc: STATUSES },
  priority: { type: 'string', value: '<p>', desc: 'low | medium | high | critical' },
  assign: { type: 'string', value: '<me|member-id>', desc: 'Assignee: "me" or a member id' },
  initiative: { type: 'string', value: '<id>', desc: 'Initiative id (omit = personal task)' },
  due: { type: 'string', value: '<YYYY-MM-DD>', desc: 'Due date' },
  'story-points': { type: 'string', value: '<n>', desc: 'Estimate 0–999 (scrum workspaces)' },
} as const

async function taskPayload(ctx: Ctx, { update }: { update: boolean }) {
  const v = ctx.values
  const nul = (x: string | undefined) => (update ? nullable(x) : x)
  return compact({
    title: str(v, 'title'),
    description: await textInput(ctx, 'description', 'description-file'),
    status: str(v, 'status'),
    priority: str(v, 'priority'),
    assigned_to: nul(str(v, 'assign')),
    initiative_id: nul(str(v, 'initiative')),
    due_date: nul(str(v, 'due')),
    story_points: num(str(v, 'story-points'), 'story-points', { allowNull: update }),
  })
}

export const taskCommands: CommandDef[] = [
  {
    path: ['tasks', 'list'],
    summary: 'List tasks (newest first). Your queue: --assigned-to me',
    options: {
      status: { type: 'string', value: '<a,b>', desc: `Comma-separated: ${STATUSES}` },
      'assigned-to': { type: 'string', value: '<me|id>', desc: '"me" or a member id' },
      priority: { type: 'string', value: '<p>', desc: 'low | medium | high | critical' },
      initiative: { type: 'string', value: '<id>', desc: 'Only this initiative' },
      q: { type: 'string', value: '<text>', desc: 'Title/description contains' },
      sort: { type: 'string', value: '<key>', desc: 'created_at (default) | completed_at | rank (backlog order)' },
      estimated: { type: 'string', value: '<true|false>', desc: 'false = tasks without story points' },
      limit: { type: 'string', value: '<n>', desc: 'Max rows (default 50, server max 100)' },
      offset: { type: 'string', value: '<n>', desc: 'Skip rows (paging)' },
    },
    examples: [
      'gc tasks list --assigned-to me --status todo,in_progress',
      'gc tasks list --status backlog --sort rank --limit 1',
    ],
    async run(ctx) {
      const v = ctx.values
      const params = compact({
        status: str(v, 'status'),
        assigned_to: str(v, 'assigned-to'),
        priority: str(v, 'priority'),
        initiative_id: str(v, 'initiative'),
        q: str(v, 'q'),
        sort: str(v, 'sort'),
        estimated: str(v, 'estimated'),
        limit: str(v, 'limit') ?? '50',
        offset: str(v, 'offset'),
        // The board's lean rows (no description) — text output never shows one.
        fields: ctx.out.raw ? undefined : 'summary',
      }) as Record<string, string>
      const res = await ctx.client().listTasks(params)
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        if (rows.length === 0) return 'No tasks.'
        const footer = pageFooter(rows.length, res.meta, 'Next page: --offset {next}')
        return [...rows.map(taskLine), ...(footer ? [footer] : [])]
      })
    },
  },
  {
    path: ['tasks', 'get'],
    summary: 'Show a task: details, description, attachments and comments',
    args: '<task-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      comments: { type: 'string', value: '<all|new|none>', desc: 'Which comments to print (default all; new needs --since)' },
      since: { type: 'string', value: '<iso|15m|2h|1d>', desc: 'With --comments new (implied): only comments after this' },
    },
    examples: ['gc tasks get <task-id>', 'gc tasks get <task-id> --since 2h', 'gc tasks get <task-id> --field description'],
    async run(ctx) {
      const id = ctx.args[0]
      let mode = str(ctx.values, 'comments') ?? (str(ctx.values, 'since') ? 'new' : 'all')
      if (!['all', 'new', 'none'].includes(mode)) throw new UsageError('--comments must be all, new or none.')
      const sinceRaw = str(ctx.values, 'since')
      if (mode === 'new' && !sinceRaw) throw new UsageError('--comments new needs --since <iso|15m|2h|1d>.')
      const since = sinceRaw ? parseSince(sinceRaw) : undefined
      const client = ctx.client()
      const [taskRes, commentsRes, attachmentsRes] = await Promise.all([
        client.getTask(id),
        mode === 'none' ? Promise.resolve({ data: [] }) : client.listComments(id),
        client.listAttachments(id),
      ])
      let comments: any[] = commentsRes?.data ?? []
      const total = comments.length
      if (since) comments = comments.filter((c) => Date.parse(c.created_at) > Date.parse(since))
      const attachments: any[] = attachmentsRes?.data ?? []
      const merged = { ...taskRes, data: { ...(taskRes?.data ?? {}), comments, attachments } }
      ctx.out.emit(merged, () => {
        const t = merged.data as any
        const meta = [
          t.status,
          t.priority,
          name(t.assignee) ? `@${name(t.assignee)}` : 'unassigned',
          t.initiative?.name ? `#${t.initiative.name}` : 'personal',
          t.due_date ? `due ${t.due_date}` : null,
          t.story_points != null ? `${t.story_points}sp` : null,
          `updated ${fmtTime(t.updated_at)}`,
        ].filter(Boolean)
        const lines = [`# ${t.title}`, `${t.id} · ${meta.join(' · ')}`, '']
        lines.push(t.description ? String(t.description).trimEnd() : '(no description)')
        if (attachments.length) {
          lines.push('', `## Attachments (${attachments.length})`)
          for (const a of attachments) lines.push(`- ${a.file_name} (${humanSize(a.file_size)}) ${a.file_url ?? ''}`.trimEnd())
        }
        if (mode !== 'none') {
          const label = since ? `${comments.length} new since ${fmtTime(since)}, ${total} total` : `${total}`
          lines.push('', `## Comments (${label})`)
          for (const c of comments) lines.push(commentBlock(c))
        }
        return lines
      })
    },
  },
  {
    path: ['tasks', 'create'],
    summary: 'Create a task',
    options: { title: { type: 'string', value: '<text>', desc: 'Title (required)' }, ...taskFieldFlags },
    examples: [
      'gc tasks create --title "Fix login redirect" --priority high --assign me',
      'gc tasks create --title "Write report" --description-file - < notes.md',
    ],
    async run(ctx) {
      requireStr(ctx.values, 'title')
      const res = await ctx.client().createTask(await taskPayload(ctx, { update: false }))
      ctx.out.emit(res, () => `created  ${taskLine(res.data)}`)
    },
  },
  {
    path: ['tasks', 'update'],
    summary: 'Update a task (only the flags you pass change; "none" clears assign/initiative/due/story-points)',
    args: '<task-id>',
    minArgs: 1,
    maxArgs: 1,
    options: { title: { type: 'string', value: '<text>', desc: 'New title' }, ...taskFieldFlags },
    details: 'In a scrum workspace (gc context) finish with --status review, never done.',
    examples: ['gc tasks update <task-id> --status in_progress', 'gc tasks update <task-id> --status review', 'gc tasks update <task-id> --assign none'],
    async run(ctx) {
      const payload = await taskPayload(ctx, { update: true })
      if (Object.keys(payload).length === 0) throw new UsageError('Nothing to update — pass at least one flag.', 'See gc tasks update --help.')
      const res = await ctx.client().updateTask(ctx.args[0], payload)
      ctx.out.emit(res, () => `updated  ${taskLine(res.data)}`)
    },
  },
  {
    path: ['comment'],
    summary: 'Post a Markdown comment on a task (progress, results, blockers)',
    args: '<task-id> [<body> | -]',
    minArgs: 1,
    maxArgs: 2,
    options: { 'body-file': { type: 'string', value: '<path|->', desc: 'Read the Markdown body from a file, or stdin with -' } },
    examples: [
      'gc comment <task-id> "Started — reproducing the bug now."',
      "gc comment <task-id> --body-file - <<'EOF'\n  ## Done\n  - PR: https://github.com/…\n  EOF",
    ],
    async run(ctx) {
      const body = await bodyInput(ctx, ctx.args[1])
      const res = await ctx.client().createComment(ctx.args[0], body)
      ctx.out.emit(res, () => `commented on ${ctx.args[0]} (${res?.data?.id ?? 'ok'})`)
    },
  },
  {
    path: ['attach'],
    summary: 'Attach a local file to a task (any type, up to 50 MB)',
    args: '<task-id> <path>',
    minArgs: 2,
    maxArgs: 2,
    options: { name: { type: 'string', value: '<file-name>', desc: 'Store it under this name' } },
    examples: ['gc attach <task-id> ./screenshot.png', 'gc attach <task-id> build.log --name build-2026-10-02.log'],
    async run(ctx) {
      const [taskId, path] = ctx.args
      if (!existsSync(path)) throw new CliError(`No such file: ${path}`)
      const res = await ctx.client().uploadAttachment(taskId, path, str(ctx.values, 'name'))
      const a = res?.data ?? {}
      ctx.out.emit(res, () => `attached ${a.file_name ?? basename(path)} (${humanSize(a.file_size)}) to ${taskId}`)
    },
  },
]
