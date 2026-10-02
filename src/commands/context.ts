import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { str, type CommandDef } from '../command'
import { atomicWrite } from '../config'
import { apiGet } from '../http'
import { CliError } from '../errors'
import { fmtTime, oneLine, pageFooter, taskLine } from '../output'
import { parseSince } from '../since'

const OPEN = 'backlog,scheduled,todo,in_progress,blocked,review'

export function workflowRule(workflow: string | undefined): string {
  return workflow === 'scrum'
    ? 'Workflow: scrum — finish your tasks with status review (a human sets done). backlog = parked: never start it.'
    : 'Workflow: kanban — finish your tasks with status done. backlog = parked: never start it.'
}

function featureList(f: unknown): string[] {
  if (Array.isArray(f)) return f.map(String)
  if (f && typeof f === 'object') return Object.entries(f).filter(([, on]) => on === true).map(([k]) => k)
  return []
}

export const contextCommands: CommandDef[] = [
  {
    path: ['context'],
    summary: 'Who am I, which workspace, and my open assigned tasks — run this first',
    examples: ['gc context', 'gc context --field me.tenant.workflow'],
    async run(ctx) {
      const client = ctx.client()
      const [me, tasks] = await Promise.all([
        client.getMe(),
        client.listTasks({ assigned_to: 'me', status: OPEN, limit: 100, fields: 'summary' }),
      ])
      const result = { me: me?.data, assigned_tasks: tasks?.data ?? [], meta: tasks?.meta }
      ctx.out.emit(result, () => {
        const u = me?.data?.user ?? {}
        const t = me?.data?.tenant ?? {}
        const rows: any[] = tasks?.data ?? []
        const features = featureList(t.features)
        const profile = ctx.auth().profileName
        const lines = [
          `${u.display_name ?? '?'} (${u.is_agent ? 'agent' : 'human'}, ${u.role ?? '?'}) in workspace "${t.name ?? '?'}"${profile ? ` · profile ${profile}` : ''}`,
          workflowRule(t.workflow),
          features.length ? `Features: ${features.join(', ')}` : '',
          '',
          `Open tasks assigned to me (${rows.length}${typeof tasks?.meta?.total === 'number' && tasks.meta.total > rows.length ? ` of ${tasks.meta.total}` : ''}):`,
          ...(rows.length ? rows.map((r) => '  ' + taskLine(r)) : ['  (none)']),
          '',
          'Next: gc changes --since 1h · gc tasks get <id> · gc guide',
        ]
        return lines.filter((l, i) => l !== '' || (i > 0 && lines[i - 1] !== ''))
      })
    },
  },
  {
    path: ['changes'],
    summary: 'What changed for me since a point in time (comments, tasks, docs, datasheets, goal check-ins)',
    options: {
      since: { type: 'string', value: '<iso|15m|2h|1d>', desc: 'Start of the window (default: the cursor file, else 1h)' },
      'cursor-file': { type: 'string', value: '<path>', desc: 'Read "since" from this file and write the new cursor back after a successful call' },
    },
    details: [
      'There is no implicit stored cursor: every poller owns its own --cursor-file, or passes --since.',
      'Items marked [principal] concern the human you assist — do not start them, tell them in the session.',
    ].join('\n'),
    examples: ['gc changes --since 2h', 'gc changes --cursor-file .gc-cursor', 'gc changes --since 2026-10-02T06:00:00Z --json'],
    async run(ctx) {
      const cursorFile = str(ctx.values, 'cursor-file')
      const sinceFlag = str(ctx.values, 'since')
      let since: string
      if (sinceFlag) since = parseSince(sinceFlag)
      else if (cursorFile && existsSync(cursorFile)) {
        const raw = readFileSync(cursorFile, 'utf8').trim()
        try {
          since = parseSince(raw)
        } catch {
          throw new CliError(`Cursor file ${cursorFile} does not hold a timestamp ("${oneLine(raw, 40)}").`, 'Pass --since to reset it.')
        }
      } else {
        since = parseSince('1h')
        if (cursorFile) ctx.out.err(`note: ${cursorFile} does not exist yet — using the last hour.`)
      }

      const res = await ctx.client().getChanges(since)

      const cursor = cursorFile ? serverCursor(res) : undefined
      if (cursorFile && !cursor) throw new CliError('The server returned no cursor (meta.cursor / meta.checked_at); the cursor file was not changed.')

      // Print first, advance the cursor last: a failing --field or a closed
      // pipe must not move the cursor past items nobody saw.
      ctx.out.emit(res, () => formatChanges(res, since, cursor))
      if (cursorFile && cursor) {
        mkdirSync(dirname(cursorFile), { recursive: true })
        atomicWrite(cursorFile, cursor + '\n', 0o600)
      }
    },
  },
  {
    path: ['members'],
    summary: 'People and agents in this workspace — the ids --assign and @-mentions need',
    options: {
      agents: { type: 'boolean', desc: 'Only agents' },
      humans: { type: 'boolean', desc: 'Only humans' },
    },
    examples: ['gc members', 'gc members --humans'],
    async run(ctx) {
      const isAgent = ctx.values.agents === true ? '?is_agent=true' : ctx.values.humans === true ? '?is_agent=false' : ''
      const res = await apiGet(ctx, `/members${isAgent}`)
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        if (!rows.length) return 'No members.'
        return rows.map((m) => `${m.id}  ${m.display_name}  (${m.is_agent ? 'agent' : m.role})`)
      })
    },
  },
]

/**
 * The next `since`, always from the server's clock: `meta.cursor` is taken
 * BEFORE the server's queries run (rows committed during the request land in
 * the next window); `checked_at` is the fallback for older servers. Never the
 * local clock — it drifts against the database's.
 */
export function serverCursor(res: any): string | undefined {
  const c = res?.meta?.cursor ?? res?.meta?.checked_at
  return typeof c === 'string' && !Number.isNaN(Date.parse(c)) ? c : undefined
}

export function formatChanges(res: any, since: string, cursor?: string): string[] {
  const d = res?.data ?? {}
  const lines: string[] = [`Changes since ${fmtTime(since)}${cursor ? ` (cursor saved: ${cursor})` : ''}`]
  let count = 0
  const principal = Object.values(d).some((v) => Array.isArray(v) && v.some((x: any) => x?.for === 'principal'))
  const tag = (x: any) => (x?.for === 'principal' ? '[principal] ' : '')
  const section = (title: string, items: any[] | undefined, fmt: (x: any) => string | string[]) => {
    if (!Array.isArray(items) || items.length === 0) return
    count += items.length
    lines.push('', `${title} (${items.length}):`)
    for (const x of items) {
      const f = fmt(x)
      for (const l of Array.isArray(f) ? f : [f]) lines.push('  ' + l)
    }
  }

  section('Task comments', d.task_comments, (c) => [
    `${tag(c)}task ${c.task_id} "${c.task_title ?? ''}" · ${fmtTime(c.created_at)}`,
    `  ${oneLine(c.content, 300)}`,
  ])
  section('Tasks created', d.tasks_created, (t) => taskLine(t))
  section('Tasks updated', d.tasks_updated, (t) => `${taskLine(t)}  · ${fmtTime(t.updated_at)}`)
  section('Doc comments', d.doc_comments, (c) => [`${tag(c)}doc ${c.doc_id} · ${fmtTime(c.created_at)}`, `  ${oneLine(c.body, 300)}`])
  section('Docs updated', d.docs_updated, (x) => `${tag(x)}${x.id}  ${x.title} · ${fmtTime(x.updated_at)}`)
  section('Datasheet rows created', d.table_rows_created, (r) => `${tag(r)}datasheet ${r.table_id} "${r.table_name ?? ''}" row ${r.id ?? r.row_id ?? ''}`)
  section('Datasheet rows updated', d.table_rows_updated, (r) => `${tag(r)}datasheet ${r.table_id} "${r.table_name ?? ''}" row ${r.id ?? r.row_id ?? ''}`)
  section('Datasheet comments', d.table_comments, (c) => [`${tag(c)}datasheet ${c.table_id} "${c.table_name ?? ''}" · ${fmtTime(c.created_at)}`, `  ${oneLine(c.body, 300)}`])
  const checkins: any[] = d.goal_checkins ?? []
  section('Goal check-ins', checkins, (g) => {
    const i = checkins.indexOf(g)
    if (g.kind === 'reply') {
      return [`reply on check-in ${g.checkin_id ?? g.id} for goal "${g.goal?.title ?? ''}" (${g.goal?.id ?? ''})`, `  ${oneLine(g.owner_reply, 300)}`]
    }
    return [
      `${g.kind} check-in ${g.id} for goal "${g.goal?.title ?? ''}" (${g.goal?.id ?? ''})${g.signals?.length ? ` · signals: ${g.signals.map((s: any) => s.code ?? s).join(', ')}` : ''}`,
      `  read: gc changes --since ${since} --field data.goal_checkins.${i}   (follow its playbook)`,
      `  submit: gc goals checkin-submit ${g.goal?.id ?? '<goal-id>'} ${g.id} --body-file -`,
    ]
  })

  if (count === 0) lines.push('Nothing new.')
  if (principal) lines.push('', '[principal] = concerns the human you assist: do NOT start it — tell them about it in this session.')
  return lines
}
