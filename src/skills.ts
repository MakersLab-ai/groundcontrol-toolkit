import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import groundcontrolSkill from '../skills/groundcontrol/SKILL.md' with { type: 'text' }
import datasheetsSkill from '../skills/groundcontrol-datasheets/SKILL.md' with { type: 'text' }
import { str, type CommandDef } from './command'
import { CliError, UsageError } from './errors'

/** The .md files under skills/ are canonical; these are their compiled-in copies (a test pins equality). */
export const SKILLS: { name: string; content: string }[] = [
  { name: 'groundcontrol', content: groundcontrolSkill },
  { name: 'groundcontrol-datasheets', content: datasheetsSkill },
]

/** Agent → its skills directory, relative to $HOME. The agent "exists" when its home dir does. */
export const AGENT_DIRS: Record<string, { home: string; skills: string }> = {
  claude: { home: '.claude', skills: '.claude/skills' },
  codex: { home: '.codex', skills: '.codex/skills' },
  openclaw: { home: '.openclaw', skills: '.openclaw/skills' },
}

export function skillDescription(content: string): string {
  return /^description:\s*(.+)$/m.exec(content)?.[1]?.trim() ?? ''
}

export const skillCommands: CommandDef[] = [
  {
    path: ['skills', 'list'],
    summary: 'List the agent skills built into gc',
    examples: ['gc skills list'],
    async run(ctx) {
      const rows = SKILLS.map((s) => ({ name: s.name, bytes: Buffer.byteLength(s.content), description: skillDescription(s.content) }))
      ctx.out.emit(rows, () => rows.map((r) => `${r.name}  (${r.bytes} B)\n  ${r.description}`))
    },
  },
  {
    path: ['skills', 'install'],
    summary: 'Install the gc skills for Claude Code, Codex and/or OpenClaw',
    options: {
      agent: { type: 'string', value: '<claude|codex|openclaw|all>', desc: 'Target agent (default all = every agent installed here)' },
      dir: { type: 'string', value: '<path>', desc: 'Install into <path>/<skill>/SKILL.md instead' },
      force: { type: 'boolean', desc: 'Overwrite skills that differ from the built-in version' },
    },
    details: 'Writes ~/.claude/skills/<name>/SKILL.md (and ~/.codex/…, ~/.openclaw/…). With --agent all, only agents whose home dir exists.',
    examples: ['gc skills install', 'gc skills install --agent claude --force', 'gc skills install --dir ./.claude/skills'],
    async run(ctx) {
      const home = ctx.sys.env.HOME || homedir()
      const agent = str(ctx.values, 'agent') ?? 'all'
      const dir = str(ctx.values, 'dir')
      let targets: { agent: string; dir: string }[]
      if (dir) targets = [{ agent: 'dir', dir }]
      else if (agent === 'all') {
        targets = Object.entries(AGENT_DIRS)
          .filter(([, d]) => existsSync(join(home, d.home)))
          .map(([a, d]) => ({ agent: a, dir: join(home, d.skills) }))
        if (!targets.length) throw new CliError('No Claude Code, Codex or OpenClaw home directory found.', 'Pass --agent claude|codex|openclaw or --dir <path>.')
      } else if (AGENT_DIRS[agent]) targets = [{ agent, dir: join(home, AGENT_DIRS[agent].skills) }]
      else throw new UsageError(`Unknown --agent "${agent}". Use claude, codex, openclaw or all.`)

      const results: { agent: string; skill: string; path: string; status: 'installed' | 'updated' | 'unchanged' | 'skipped' }[] = []
      for (const t of targets) {
        for (const s of SKILLS) {
          const path = join(t.dir, s.name, 'SKILL.md')
          let status: (typeof results)[number]['status'] = 'installed'
          if (existsSync(path)) {
            const current = readFileSync(path, 'utf8')
            if (current === s.content) status = 'unchanged'
            else status = ctx.values.force === true ? 'updated' : 'skipped'
          }
          if (status === 'installed' || status === 'updated') {
            mkdirSync(join(t.dir, s.name), { recursive: true })
            writeFileSync(path, s.content)
          }
          results.push({ agent: t.agent, skill: s.name, path, status })
        }
      }
      ctx.out.emit(results, () => {
        const lines = results.map((r) => `${r.status.padEnd(9)} ${r.path}`)
        if (results.some((r) => r.status === 'skipped')) lines.push('Skipped files differ from this gc version — rerun with --force to overwrite them.')
        return lines
      })
    },
  },
]
