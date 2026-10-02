import { compact, jsonInput, list, num, requireStr, str, textInput, type CommandDef } from '../command'
import { UsageError } from '../errors'
import { fmtTime, name, pad, pageFooter } from '../output'

const goalLine = (g: any) =>
  [
    g.id,
    pad(g.status, 8),
    `${Math.round(Number(g.progress ?? 0))}%`.padStart(4),
    g.title,
    g.initiative?.name ? `#${g.initiative.name}` : '',
    name(g.owner) ? `owner ${name(g.owner)}` : '',
    name(g.agent) ? `agent ${name(g.agent)}` : '',
  ]
    .filter(Boolean)
    .join('  ')

/** `--kr "Title|target|unit"` → key result input; target/unit optional. */
export function parseKr(spec: string): { title: string; target_value?: number; unit?: string } {
  const [title, target, unit] = spec.split('|').map((s) => s.trim())
  if (!title) throw new UsageError(`--kr "${spec}": the title is empty. Format: "Title|target|unit".`)
  const t = target ? Number(target) : undefined
  if (target && !Number.isFinite(t)) throw new UsageError(`--kr "${spec}": target "${target}" is not a number.`)
  return compact({ title, target_value: t, unit: unit || undefined }) as { title: string }
}

const goalFlags = {
  description: { type: 'string', value: '<md>', desc: 'Description' },
  'description-file': { type: 'string', value: '<path|->', desc: 'Read the description from a file, or stdin with -' },
  initiative: { type: 'string', value: '<id>', desc: 'Home initiative (sets who can see the goal)' },
  owner: { type: 'string', value: '<member-id>', desc: 'Human who owns the goal' },
  agent: { type: 'string', value: '<member-id>', desc: 'Agent that runs the weekly check-ins' },
  autonomy: { type: 'string', value: '<approve|act>', desc: 'approve (default): planned tasks wait in backlog; act: start right away' },
} as const

export const goalCommands: CommandDef[] = [
  {
    path: ['goals', 'list'],
    summary: 'List goals with progress',
    options: {
      status: { type: 'string', value: '<s>', desc: 'active | achieved | missed | archived' },
      initiative: { type: 'string', value: '<id>', desc: 'Only this initiative' },
      mine: { type: 'boolean', desc: 'Only goals where I am the agent' },
    },
    examples: ['gc goals list', 'gc goals list --mine'],
    async run(ctx) {
      const v = ctx.values
      const fetched = await ctx.client().listObjectives(compact({
        status: str(v, 'status'), agent: v.mine === true ? 'me' : undefined, limit: '100',
      }))
      // The route has no initiative filter; narrow here.
      const initiative = str(v, 'initiative')
      const res = initiative ? { ...fetched, data: (fetched?.data ?? []).filter((g: any) => g.initiative_id === initiative) } : fetched
      ctx.out.emit(res, () => {
        const rows: any[] = res?.data ?? []
        if (!rows.length) return 'No goals.'
        const footer = initiative ? null : pageFooter(rows.length, res.meta, 'Next page: not supported here — narrow with --status/--mine.')
        return [...rows.map(goalLine), ...(footer ? [footer] : [])]
      })
    },
  },
  {
    path: ['goals', 'get'],
    summary: 'Show a goal with its key results and latest check-in',
    args: '<goal-id>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc goals get <goal-id>'],
    async run(ctx) {
      const res = await ctx.client().getObjective(ctx.args[0])
      ctx.out.emit(res, () => {
        const g = res.data
        const lines = [`# ${g.title}`, `${goalLine(g)} · autonomy ${g.autonomy ?? '-'}`]
        if (g.description) lines.push('', String(g.description).trimEnd())
        const krs: any[] = g.key_results ?? []
        lines.push('', `## Key results (${krs.length})`)
        for (const kr of krs) lines.push(`- ${kr.id}  ${kr.title}  ${kr.current_value}/${kr.target_value}${kr.unit ? ' ' + kr.unit : ''}  (score ${kr.score})`)
        const c = g.latest_checkin
        if (c) lines.push('', `Latest check-in: ${c.kind ?? ''} ${c.status ?? ''} ${c.confidence ?? ''} · week ${c.week_start ?? '-'}${c.submitted_at ? ` · submitted ${fmtTime(c.submitted_at)}` : ''}`.trimEnd())
        return lines
      })
    },
  },
  {
    path: ['goals', 'create'],
    summary: 'Create a goal (owner, agent and initiative are required)',
    options: {
      title: { type: 'string', value: '<text>', desc: 'Title (required)' },
      ...goalFlags,
      kr: { type: 'string', multiple: true, value: '<"Title|target|unit">', desc: 'Key result (repeatable)' },
    },
    examples: ['gc goals create --title "Grow the newsletter" --initiative <initiative-id> --owner <owner-id> --agent <agent-id> --kr "Subscribers|5000|subs"'],
    async run(ctx) {
      const v = ctx.values
      const body = compact({
        title: requireStr(v, 'title'),
        description: await textInput(ctx, 'description', 'description-file'),
        initiative_id: requireStr(v, 'initiative'),
        owner_member_id: requireStr(v, 'owner'),
        agent_member_id: requireStr(v, 'agent'),
        autonomy: str(v, 'autonomy'),
        key_results: list(v, 'kr').length ? list(v, 'kr').map(parseKr) : undefined,
      })
      const res = await ctx.client().createObjective(body)
      ctx.out.emit(res, () => `created  ${goalLine(res.data)}`)
    },
  },
  {
    path: ['goals', 'update'],
    summary: "Update a goal. As the goal's own agent: only with your human's explicit approval",
    args: '<goal-id>',
    minArgs: 1,
    maxArgs: 1,
    options: {
      title: { type: 'string', value: '<text>', desc: 'New title' },
      status: { type: 'string', value: '<s>', desc: 'active | achieved | missed | archived' },
      ...goalFlags,
    },
    examples: ['gc goals update <goal-id> --status achieved'],
    async run(ctx) {
      const v = ctx.values
      const patch = compact({
        title: str(v, 'title'),
        description: await textInput(ctx, 'description', 'description-file'),
        status: str(v, 'status'),
        initiative_id: str(v, 'initiative'),
        owner_member_id: str(v, 'owner'),
        agent_member_id: str(v, 'agent'),
        autonomy: str(v, 'autonomy'),
      })
      if (!Object.keys(patch).length) throw new UsageError('Nothing to update — pass at least one flag.')
      const res = await ctx.client().updateObjective(ctx.args[0], patch)
      ctx.out.emit(res, () => `updated  ${goalLine(res.data)}`)
    },
  },
  {
    path: ['goals', 'kr-update'],
    summary: 'Record key result progress (--current-value) or a manual score; title/target/unit need approval',
    args: '<goal-id> <kr-id>',
    minArgs: 2,
    maxArgs: 2,
    options: {
      'current-value': { type: 'string', value: '<n>', desc: 'Current value (score is computed)' },
      current: { type: 'string', value: '<n>', desc: 'Alias of --current-value' },
      score: { type: 'string', value: '<0..1>', desc: 'Manual score override' },
      title: { type: 'string', value: '<text>', desc: 'New title (with approval)' },
      target: { type: 'string', value: '<n>', desc: 'New target (with approval)' },
      unit: { type: 'string', value: '<unit>', desc: 'New unit (with approval)' },
    },
    examples: ['gc goals kr-update <goal-id> <kr-id> --current-value 3200'],
    async run(ctx) {
      const v = ctx.values
      const patch = compact({
        current_value: num(str(v, 'current-value') ?? str(v, 'current'), 'current-value') ?? undefined,
        score: num(str(v, 'score'), 'score') ?? undefined,
        title: str(v, 'title'),
        target_value: num(str(v, 'target'), 'target') ?? undefined,
        unit: str(v, 'unit'),
      })
      if (!Object.keys(patch).length) throw new UsageError('Nothing to update — pass --current-value, --score, --title, --target or --unit.')
      const res = await ctx.client().updateKeyResult(ctx.args[0], ctx.args[1], patch)
      ctx.out.emit(res, () => `updated key result ${ctx.args[1]}`)
    },
  },
  {
    path: ['goals', 'checkin-submit'],
    summary: 'Submit a weekly/kickoff goal check-in (JSON body, see the item\'s playbook)',
    args: '<goal-id> <checkin-id>',
    minArgs: 2,
    maxArgs: 2,
    options: {
      'body-file': { type: 'string', value: '<path|->', desc: 'JSON body from a file, or stdin with -' },
      data: { type: 'string', value: '<json>', desc: 'JSON body inline' },
    },
    details: [
      'Body: { confidence: on_track|at_risk|off_track, summary, research (required),',
      '  recommendation?: continue|adjust|rethink (required when the item has signals), recommendation_reason?,',
      '  kr_updates?: [{kr_id, current_value, note?}], kr_proposals?: [...], effects?: [{task_id, effect, note?}],',
      '  tasks?: [{title, hypothesis, description?, key_result_id?, priority?}] }',
      'Details: gc guide goals',
    ].join('\n'),
    examples: ["gc goals checkin-submit <goal-id> <checkin-id> --body-file - <<'EOF'\n  {\"confidence\":\"on_track\",\"summary\":\"…\",\"research\":\"…\"}\n  EOF"],
    async run(ctx) {
      const body = await jsonInput(ctx, 'data', 'body-file')
      if (!body || typeof body !== 'object' || Array.isArray(body)) throw new UsageError('The check-in body must be a JSON object.')
      const res = await ctx.client().submitGoalCheckin(ctx.args[0], ctx.args[1], body)
      ctx.out.emit(res, () => `submitted check-in ${ctx.args[1]}`)
    },
  },
]
