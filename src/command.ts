import { readFileSync } from 'node:fs'
import { GroundControlClient } from './client'
import { requireKey, resolveAuth, type ConfigFile, type ResolvedAuth } from './config'
import { CliError, UsageError } from './errors'
import { Output } from './output'

export interface OptSpec {
  type: 'string' | 'boolean'
  short?: string
  multiple?: boolean
  /** Placeholder shown in help, e.g. `<id>`. */
  value?: string
  desc: string
}

export type Values = Record<string, string | boolean | string[] | undefined>

export interface Sys {
  env: Record<string, string | undefined>
  stdout: (s: string) => void
  stderr: (s: string) => void
  stdinIsTTY: boolean
  readStdin: () => Promise<string>
  promptHidden: (question: string) => Promise<string>
  /** SIGINT/SIGTERM subscription (long-running commands); returns the unsubscribe. */
  onSignal?: (handler: (signal: string) => void) => () => void
}

export interface Ctx {
  values: Values
  args: string[]
  out: Output
  sys: Sys
  command: CommandDef
  /** Resolved key/url/profile (lazy: only commands that talk to the API need it). */
  auth: (cfg?: ConfigFile) => ResolvedAuth
  client: () => GroundControlClient
}

export interface CommandDef {
  path: string[]
  summary: string
  /** Positional synopsis for help, e.g. `<task-id> [<body>|-]`. */
  args?: string
  minArgs?: number
  maxArgs?: number
  options?: Record<string, OptSpec>
  examples?: string[]
  /** Long-form note printed under the usage line. */
  details?: string
  run: (ctx: Ctx) => Promise<void>
}

export const GLOBAL_OPTIONS: Record<string, OptSpec> = {
  json: { type: 'boolean', desc: 'Print the raw API response as JSON' },
  field: { type: 'string', value: '<path>', desc: 'Print one value, e.g. --field data.status (or just status)' },
  profile: { type: 'string', value: '<name>', desc: 'Use this profile (default: GC_PROFILE, then the current one)' },
  token: { type: 'string', value: '<key>', desc: 'API key for this call (overrides GC_API_KEY and the profile)' },
  'api-url': { type: 'string', value: '<url>', desc: 'API base URL (default: GC_API_URL, profile, production)' },
  'session-id': { type: 'string', value: '<id>', desc: 'Agent session id (GC_SESSION_ID) — shows the "working" badge' },
  help: { type: 'boolean', short: 'h', desc: 'Show help for this command' },
}

export function makeCtx(command: CommandDef, values: Values, args: string[], sys: Sys): Ctx {
  const out = new Output({ json: values.json === true, field: str(values, 'field') }, { stdout: sys.stdout, stderr: sys.stderr })
  let client: GroundControlClient | undefined
  const auth = (cfg?: ConfigFile) =>
    resolveAuth({ token: str(values, 'token'), profile: str(values, 'profile'), 'api-url': str(values, 'api-url') }, sys.env, cfg)
  return {
    values,
    args,
    out,
    sys,
    command,
    auth,
    client: () => {
      if (!client) {
        const a = auth()
        client = new GroundControlClient({
          apiUrl: a.apiUrl,
          apiKey: requireKey(a),
          sessionId: str(values, 'session-id') || sys.env.GC_SESSION_ID || undefined,
        })
      }
      return client
    },
  }
}

// ─── value helpers ────────────────────────────────────────────────────────────

/** A string flag, or undefined when absent or empty — empty optional flags are not sent. */
export function str(values: Values, key: string): string | undefined {
  const v = values[key]
  if (typeof v !== 'string') return undefined
  return v === '' ? undefined : v
}

export function list(values: Values, key: string): string[] {
  const v = values[key]
  if (Array.isArray(v)) return v.filter((x) => x !== '')
  if (typeof v === 'string' && v !== '') return [v]
  return []
}

export function requireStr(values: Values, key: string): string {
  const v = str(values, key)
  if (v === undefined) throw new UsageError(`Missing required flag --${key}.`)
  return v
}

/** `none`/`null` → null (clears the field on PATCH), anything else passes through. */
export function nullable(v: string | undefined): string | null | undefined {
  if (v === undefined) return undefined
  return v === 'none' || v === 'null' ? null : v
}

export function num(v: string | undefined, flag: string, { allowNull = false } = {}): number | null | undefined {
  if (v === undefined) return undefined
  if (allowNull && (v === 'none' || v === 'null')) return null
  const n = Number(v)
  if (v.trim() === '' || !Number.isFinite(n)) throw new UsageError(`--${flag} must be a number, got "${v}".`)
  return n
}

/** Copy only the keys whose value is not undefined. */
export function compact<T extends Record<string, unknown>>(obj: T): Partial<T> {
  const r: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(obj)) if (v !== undefined) r[k] = v
  return r as Partial<T>
}

/** `-` reads stdin, anything else is a file path. */
export async function readSource(ctx: Ctx, pathOrDash: string): Promise<string> {
  if (pathOrDash === '-') return ctx.sys.readStdin()
  try {
    return readFileSync(pathOrDash, 'utf8')
  } catch (e) {
    throw new CliError(`Cannot read ${pathOrDash}: ${(e as Error).message}`)
  }
}

/**
 * Text that may come inline (`--description "…"`), from a file
 * (`--description-file notes.md`) or stdin (`--description-file -`).
 */
export async function textInput(ctx: Ctx, inlineKey: string, fileKey: string): Promise<string | undefined> {
  const inline = ctx.values[inlineKey]
  const file = str(ctx.values, fileKey)
  if (typeof inline === 'string' && file) throw new UsageError(`Use either --${inlineKey} or --${fileKey}, not both.`)
  if (file) return readSource(ctx, file)
  return typeof inline === 'string' && inline !== '' ? inline : undefined
}

/** A comment body: positional text, positional `-` (stdin), or --body-file. */
export async function bodyInput(ctx: Ctx, positional: string | undefined): Promise<string> {
  const file = str(ctx.values, 'body-file')
  if (positional !== undefined && file) throw new UsageError('Give the body either as an argument or with --body-file, not both.')
  let body: string | undefined
  if (file) body = await readSource(ctx, file)
  else if (positional === '-') body = await ctx.sys.readStdin()
  else body = positional
  if (body === undefined || body.trim() === '') {
    throw new UsageError('The body is empty.', 'Pass it as an argument, or Markdown via --body-file <path> / --body-file - (stdin).')
  }
  return body.replace(/\s+$/, '')
}

/** JSON from `--data '<json>'`, `--data-file <path>` or `--data-file -`. */
export async function jsonInput(ctx: Ctx, inlineKey = 'data', fileKey = 'data-file'): Promise<unknown> {
  const raw = await textInput(ctx, inlineKey, fileKey)
  if (raw === undefined) throw new UsageError(`Missing --${inlineKey} '<json>' or --${fileKey} <path|->.`)
  return parseJson(raw, `--${inlineKey}/--${fileKey}`)
}

export function parseJson(raw: string, what: string): unknown {
  try {
    return JSON.parse(raw)
  } catch (e) {
    throw new UsageError(`${what} is not valid JSON: ${(e as Error).message}`)
  }
}
