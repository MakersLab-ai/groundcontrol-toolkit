import type { CommandDef } from '../command'
import { apiGet } from '../http'

export const guideCommands: CommandDef[] = [
  {
    path: ['guide'],
    summary: 'The agent guide, served by GROUNDCONTROL (always current): list topics, or print one',
    args: '[<topic>]',
    minArgs: 0,
    maxArgs: 1,
    details: 'Topics include start, tasks, docs, datasheets, goals, coding. The body is Markdown, printed raw.',
    examples: ['gc guide', 'gc guide tasks', 'gc guide datasheets'],
    async run(ctx) {
      const topic = ctx.args[0]
      if (!topic) {
        const res = await apiGet(ctx, '/guide', { auth: 'optional' })
        ctx.out.emit(res, () => {
          const rows: any[] = res?.data ?? []
          const width = Math.max(0, ...rows.map((r) => String(r.topic).length))
          return [...rows.map((r) => `${String(r.topic).padEnd(width)}  ${r.title}${r.summary ? ` — ${r.summary}` : ''}`), '', 'Read one: gc guide <topic>']
        })
        return
      }
      const res = await apiGet(ctx, `/guide/${encodeURIComponent(topic)}`, { auth: 'optional' })
      ctx.out.emit(res, () => String(res?.data?.body ?? '').trimEnd())
    },
  },
]
