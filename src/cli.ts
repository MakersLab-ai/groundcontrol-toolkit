import { parseArgs } from 'node:util'
import pkg from '../package.json' with { type: 'json' }
import { GLOBAL_OPTIONS, makeCtx, type CommandDef, type OptSpec, type Sys, type Values } from './command'
import { accountCommands } from './commands/account'
import { contextCommands } from './commands/context'
import { docCommands, initiativeCommands, journalCommands, searchCommands } from './commands/content'
import { goalCommands } from './commands/goals'
import { guideCommands } from './commands/guide'
import { listenCommand } from './commands/listen'
import { tableCommands } from './commands/tables'
import { taskCommands } from './commands/tasks'
import { CliError, NotConnectedError, REJECTED, UsageError, isUnauthorized } from './errors'
import { skillCommands } from './skills'

export const VERSION: string = pkg.version

const versionCommand: CommandDef = {
  path: ['version'],
  summary: 'Print the gc version',
  examples: ['gc version'],
  async run(ctx) {
    ctx.out.emit({ version: VERSION }, () => `gc ${VERSION}`)
  },
}

export const COMMANDS: CommandDef[] = [
  ...accountCommands,
  ...contextCommands,
  listenCommand,
  ...taskCommands,
  ...docCommands,
  ...initiativeCommands,
  ...searchCommands,
  ...goalCommands,
  ...tableCommands,
  ...journalCommands,
  ...guideCommands,
  ...skillCommands,
  versionCommand,
]

const GROUP_SUMMARY: Record<string, string> = {
  tasks: 'Tasks: list, get, create, update',
  docs: 'Documents: list, get, create, update, archive, comment',
  initiatives: 'Initiatives: list, get, memory',
  goals: 'Goals (OKRs): list, get, create, update, kr-update, checkin-submit',
  tables: 'Datasheets: list, get, rows, add-rows, update-rows, … (gc tables --help)',
  journal: 'Journal: list, get, summary',
  skills: 'Agent skills: list, install',
  profiles: 'Saved workspaces: list, use, remove',
}

const SECTIONS: { title: string; names: string[] }[] = [
  { title: 'Start', names: ['onboarding', 'context', 'changes', 'listen', 'guide'] },
  { title: 'Work', names: ['tasks', 'comment', 'attach', 'docs', 'search', 'semantic-search', 'initiatives', 'goals', 'tables', 'journal'] },
  { title: 'Setup', names: ['profiles', 'skills', 'version', 'help'] },
]

/** Global flags that take a value — needed to find the command words in argv. */
const GLOBAL_VALUE_FLAGS = new Set(
  Object.entries(GLOBAL_OPTIONS).filter(([, o]) => o.type === 'string').map(([k]) => `--${k}`),
)

function isGroup(word: string): boolean {
  return COMMANDS.some((c) => c.path.length > 1 && c.path[0] === word)
}

function findCommand(path: string[]): CommandDef | undefined {
  return COMMANDS.find((c) => c.path.length === path.length && c.path.every((p, i) => p === path[i]))
}

/** Indices of the leading positional words (skipping global flags and their values). */
function positionalIndices(argv: string[]): number[] {
  const idx: number[] = []
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--') break
    if (a.startsWith('-') && a !== '-') {
      if (GLOBAL_VALUE_FLAGS.has(a)) i++
      continue
    }
    idx.push(i)
    if (idx.length === 2) break
  }
  return idx
}

export interface Resolved {
  command?: CommandDef
  group?: string
  rest: string[]
}

export function resolveCommand(argv: string[]): Resolved {
  const idx = positionalIndices(argv)
  const words = idx.map((i) => argv[i])
  const drop = (n: number) => argv.filter((_, i) => !idx.slice(0, n).includes(i))
  if (words.length === 0) return { rest: argv }
  const [w0, w1] = words
  if (w0 === 'help') return { group: 'help', rest: drop(1) }
  if (isGroup(w0)) {
    if (w1 !== undefined) {
      const sub = findCommand([w0, w1])
      if (sub) return { command: sub, rest: drop(2) }
    }
    const solo = findCommand([w0]) // e.g. `gc profiles` lists
    if (solo && (w1 === undefined || !isSubcommandLike(w1))) return { command: solo, rest: drop(1) }
    if (w1 !== undefined) throw new UsageError(`Unknown command "gc ${w0} ${w1}".`, `Run: gc ${w0} --help`)
    return { group: w0, rest: drop(1) }
  }
  const cmd = findCommand([w0])
  if (!cmd) throw new UsageError(`Unknown command "${w0}".`, 'Run: gc help')
  return { command: cmd, rest: drop(1) }
}

function isSubcommandLike(w: string): boolean {
  return /^[a-z][a-z-]*$/.test(w)
}

function toParseOptions(specs: Record<string, OptSpec>) {
  const o: Record<string, { type: 'string' | 'boolean'; short?: string; multiple?: boolean }> = {}
  for (const [k, s] of Object.entries(specs)) o[k] = { type: s.type, ...(s.short ? { short: s.short } : {}), ...(s.multiple ? { multiple: true } : {}) }
  return o
}

export function parseCommandArgs(cmd: CommandDef, rest: string[]): { values: Values; positionals: string[] } {
  const options = { ...GLOBAL_OPTIONS, ...(cmd.options ?? {}) }
  let parsed
  try {
    parsed = parseArgs({ args: rest, options: toParseOptions(options), allowPositionals: true, strict: true })
  } catch (e) {
    const msg = (e as Error).message.replace(/\. To specify a positional argument.*$/s, '.')
    throw new UsageError(msg, `Run: gc ${cmd.path.join(' ')} --help`)
  }
  const values = parsed.values as Values
  if (values.help === true) return { values, positionals: parsed.positionals }
  const n = parsed.positionals.length
  const min = cmd.minArgs ?? 0
  const max = cmd.maxArgs ?? 0
  if (n < min) throw new UsageError(`Missing argument${cmd.args ? `: ${cmd.args}` : ''}.`, `Usage: gc ${cmd.path.join(' ')} ${cmd.args ?? ''}`.trimEnd())
  if (n > max) throw new UsageError(`Unexpected argument "${parsed.positionals[max]}".`, `Usage: gc ${cmd.path.join(' ')} ${cmd.args ?? ''}`.trimEnd() + (cmd.path.includes('comment') ? ' — quote the body, or use --body-file -' : ''))
  return { values, positionals: parsed.positionals }
}

// ─── help ─────────────────────────────────────────────────────────────────────

function flagLines(specs: Record<string, OptSpec>): string[] {
  const rows = Object.entries(specs).map(([k, s]) => [`${s.short ? `-${s.short}, ` : ''}--${k}${s.value ? ' ' + s.value : ''}`, s.desc + (s.multiple ? ' (repeatable)' : '')])
  const w = Math.min(36, Math.max(...rows.map((r) => r[0].length)))
  return rows.map(([a, b]) => `  ${a.padEnd(w)}  ${b}`)
}

export function commandHelp(cmd: CommandDef): string {
  const lines = [`gc ${cmd.path.join(' ')} — ${cmd.summary}`, '', `Usage: gc ${cmd.path.join(' ')}${cmd.args ? ' ' + cmd.args : ''}${cmd.options ? ' [flags]' : ''}`]
  if (cmd.details) lines.push('', cmd.details)
  if (cmd.options && Object.keys(cmd.options).length) lines.push('', 'Flags:', ...flagLines(cmd.options))
  if (cmd.examples?.length) lines.push('', 'Examples:', ...cmd.examples.map((e) => `  ${e}`))
  lines.push('', 'Global flags: --json, --field <path>, --profile <name>, --token <key>, --api-url <url>, --session-id <id> (gc help globals)')
  return lines.join('\n')
}

export function groupHelp(group: string): string {
  const subs = COMMANDS.filter((c) => c.path[0] === group)
  const w = Math.max(...subs.map((c) => c.path.join(' ').length))
  return [
    `gc ${group} — ${GROUP_SUMMARY[group] ?? ''}`,
    '',
    ...subs.map((c) => `  gc ${c.path.join(' ').padEnd(w)}  ${c.summary}`),
    '',
    `Help for one: gc ${group} <command> --help`,
  ].join('\n')
}

export function mainHelp(): string {
  const summaryOf = (name: string) => GROUP_SUMMARY[name] ?? findCommand([name])?.summary ?? (name === 'help' ? 'Help for a command: gc help tasks create' : '')
  const lines = [
    `gc ${VERSION} — GROUNDCONTROL for agents (alias: groundcontrol)`,
    '',
    'Usage: gc <command> [flags]',
  ]
  for (const s of SECTIONS) {
    lines.push('', `${s.title}:`)
    for (const n of s.names) lines.push(`  ${n.padEnd(16)} ${summaryOf(n)}`)
  }
  lines.push(
    '',
    'Global flags:',
    ...flagLines(GLOBAL_OPTIONS),
    '',
    'Examples:',
    '  gc onboarding --token gc_live_…',
    '  gc context',
    '  gc changes --since 2h',
    '  gc tasks get <id>',
    '  gc comment <id> --body-file - < result.md',
    '  gc guide tasks',
    '',
    'Environment: GC_API_KEY, GC_API_URL, GC_PROFILE, GC_SESSION_ID, GC_CONFIG_DIR',
    '',
    'Exit codes: 0 ok · 1 API or other error · 2 usage error · 3 not connected (no API key, or the key was rejected)',
  )
  return lines.join('\n')
}

// ─── entry ────────────────────────────────────────────────────────────────────

/** Runs one invocation and returns the exit code (0 ok, 1 failure, 2 usage). */
export async function run(argv: string[], sys: Sys): Promise<number> {
  const out = (s: string) => sys.stdout(s.endsWith('\n') ? s : s + '\n')
  const err = (s: string) => sys.stderr(s.endsWith('\n') ? s : s + '\n')
  try {
    if (argv.length === 0 || (argv.length === 1 && (argv[0] === '--help' || argv[0] === '-h'))) {
      out(mainHelp())
      return 0
    }
    if (argv.length === 1 && (argv[0] === '--version' || argv[0] === '-v')) {
      out(`gc ${VERSION}`)
      return 0
    }
    const resolved = resolveCommand(argv)
    if (resolved.group === 'help') {
      const words = resolved.rest.filter((a) => !a.startsWith('-'))
      if (words[0] === 'globals') out(['Global flags (any command):', ...flagLines(GLOBAL_OPTIONS)].join('\n'))
      else if (!words.length) out(mainHelp())
      else {
        const cmd = findCommand(words.slice(0, 2)) ?? findCommand(words.slice(0, 1))
        if (cmd) out(commandHelp(cmd))
        else if (isGroup(words[0])) out(groupHelp(words[0]))
        else throw new UsageError(`Unknown command "${words.join(' ')}".`, 'Run: gc help')
      }
      return 0
    }
    if (resolved.group) {
      out(groupHelp(resolved.group))
      // `gc tasks` with no subcommand: help is the answer, not an error.
      return resolved.rest.includes('--help') || resolved.rest.includes('-h') || resolved.rest.length === 0 ? 0 : 2
    }
    if (!resolved.command) {
      // Only flags, no command (e.g. `gc --json`).
      throw new UsageError('No command given.', 'Run: gc help')
    }
    const cmd = resolved.command
    const { values, positionals } = parseCommandArgs(cmd, resolved.rest)
    if (values.help === true) {
      out(isGroup(cmd.path[0]) && cmd.path.length === 1 ? groupHelp(cmd.path[0]) + '\n\n' + commandHelp(cmd) : commandHelp(cmd))
      return 0
    }
    if (values.json === true && typeof values.field === 'string') throw new UsageError('Use either --json or --field, not both.')
    const ctx = makeCtx(cmd, values, positionals, sys)
    try {
      await cmd.run(ctx)
    } catch (e) {
      // A rejected key is "not connected", wherever it surfaces (onboarding has its own message).
      if (isUnauthorized(e) && !(e instanceof CliError && e.exitCode === 3)) {
        let apiUrl: string | undefined
        try { apiUrl = ctx.auth().apiUrl } catch { /* unknown profile etc. */ }
        throw new NotConnectedError(REJECTED, apiUrl)
      }
      throw e
    }
    return 0
  } catch (e) {
    if (e instanceof CliError) {
      err(`gc: ${e.message}`)
      if (e.hint) err(e.hint)
      return e.exitCode
    }
    err(`gc: ${(e as Error)?.message ?? String(e)}`)
    return 1
  }
}
