import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { GroundControlClient } from '../client'
import { str, type CommandDef, type Ctx } from '../command'
import { atomicWrite, configDir, loadConfig, requireKey } from '../config'
import { CliError, NotConnectedError, REJECTED, UsageError, isUnauthorized } from '../errors'
import { parseSince } from '../since'
import { serverCursor } from './context'

// `gc listen` — the successor of openclaw-plugin/scripts/gc-worker.sh: poll
// /changes per workspace (0 tokens) and wake an agent only when something
// happened. Each workspace has ONE cursor, owned by the listener alone
// (<config dir>/listen/<name>.json). It is never shared with a session's
// `gc changes --cursor-file`: a reader that advances someone else's cursor
// consumes items before that someone sees them (LEARNINGS 2026-10-01).

export interface Target {
  name: string
  /** A saved profile (GC_PROFILE may name it for the spawned session). */
  isProfile: boolean
  apiUrl: string
  apiKey: string
}

export interface Item {
  profile: string
  kind: string
  for: string
  id: string | null
  task_id?: string
  doc_id?: string
  table_id?: string
  created_at: string | null
  title?: string
}

const MAX_BACKOFF_MS = 5 * 60_000

/** `60s`, `5m`, `1h`, or bare seconds. */
export function parseInterval(v: string): number {
  const m = /^(\d+)\s*(s|m|h)?$/i.exec(v.trim())
  if (!m) throw new UsageError(`Invalid --interval "${v}".`, 'Use e.g. 30s, 5m or 1h.')
  const ms = Number(m[1]) * ({ s: 1_000, m: 60_000, h: 3_600_000 } as Record<string, number>)[(m[2] ?? 's').toLowerCase()]
  if (ms < 5_000) throw new UsageError('--interval must be at least 5s.')
  return ms
}

/** Every array under `data` is a list of change items — new kinds count without a code change here. */
export function collectItems(profile: string, data: unknown): Item[] {
  const items: Item[] = []
  if (!data || typeof data !== 'object') return items
  for (const [kind, list] of Object.entries(data as Record<string, unknown>)) {
    if (!Array.isArray(list)) continue
    for (const x of list as any[]) {
      const item: Item = {
        profile,
        kind,
        for: x?.for ?? 'self',
        id: x?.id ?? x?.row_id ?? null,
        created_at: x?.created_at ?? x?.updated_at ?? x?.delivered_at ?? null,
      }
      if (x?.task_id) item.task_id = x.task_id
      if (x?.doc_id) item.doc_id = x.doc_id
      if (x?.table_id) item.table_id = x.table_id
      const title = x?.title ?? x?.task_title ?? x?.table_name ?? x?.goal?.title
      if (title) item.title = title
      items.push(item)
    }
  }
  return items
}

export function itemLine(i: Item): string {
  const ref = i.task_id ? `task ${i.task_id}` : i.doc_id ? `doc ${i.doc_id}` : i.table_id ? `datasheet ${i.table_id}` : ''
  return [i.profile, i.kind, `${i.for === 'principal' ? '[principal] ' : ''}${i.id ?? '-'}`, ref, i.created_at ?? '-', i.title ? `"${i.title}"` : '']
    .filter(Boolean)
    .join('  ')
}

const safeName = (n: string) => n.replace(/[^A-Za-z0-9._-]/g, '_')

export function cursorPath(env: Record<string, string | undefined>, name: string): string {
  return join(configDir(env), 'listen', `${safeName(name)}.json`)
}

function readCursor(path: string): string | undefined {
  if (!existsSync(path)) return undefined
  try {
    const c = JSON.parse(readFileSync(path, 'utf8'))?.cursor
    return typeof c === 'string' && !Number.isNaN(Date.parse(c)) ? c : undefined
  } catch {
    return undefined
  }
}

function writeCursor(path: string, cursor: string): void {
  mkdirSync(join(path, '..'), { recursive: true, mode: 0o700 })
  atomicWrite(path, JSON.stringify({ cursor }) + '\n', 0o600)
}

export function resolveTargets(ctx: Ctx): Target[] {
  const env = ctx.sys.env
  if (ctx.values['all-profiles'] === true) {
    if (str(ctx.values, 'profile') || str(ctx.values, 'token')) throw new UsageError('--all-profiles polls every saved profile; drop --profile/--token.')
    const cfg = loadConfig(env)
    const targets = Object.entries(cfg.profiles)
      .filter(([, p]) => p.api_key)
      .map(([name, p]) => ({ name, isProfile: true, apiUrl: (str(ctx.values, 'api-url') || p.api_url).replace(/\/+$/, ''), apiKey: p.api_key }))
    if (!targets.length) throw new NotConnectedError('no API key', str(ctx.values, 'api-url') || env.GC_API_URL)
    return targets
  }
  const a = ctx.auth()
  const apiKey = requireKey(a)
  // A key from --token/GC_API_KEY gets its own cursor, named by a hash (never the key itself).
  const isProfile = a.keySource === 'profile' && Boolean(a.profileName)
  const name = isProfile ? a.profileName! : `key-${createHash('sha256').update(apiKey).digest('hex').slice(0, 12)}`
  return [{ name, isProfile, apiUrl: a.apiUrl, apiKey }]
}

type PollResult = 'ok' | 'unauthorized' | 'error'

export const listenCommand: CommandDef = {
  path: ['listen'],
  summary: 'Watch for changes and wake an agent (successor of gc-worker.sh): print items, or run --exec per batch',
  options: {
    interval: { type: 'string', value: '<30s|5m>', desc: 'Time between polls (default 60s, min 5s)' },
    once: { type: 'boolean', desc: 'One pass over the profiles, then exit (for cron/launchd)' },
    exec: { type: 'string', value: '<shell cmd>', desc: 'Run this via sh -c when there are changes, and wait for it' },
    'all-profiles': { type: 'boolean', desc: 'Poll every saved profile (one cursor each)' },
    since: { type: 'string', value: '<iso|15m|2h>', desc: 'Start here instead of the stored cursor (first run default: now)' },
  },
  details: [
    'One cursor per workspace, owned by the listener: <config dir>/listen/<profile>.json. It advances to the',
    "server's meta.cursor after each successful poll (and, with --exec, once the command has started).",
    '--exec gets: GC_PROFILE, GC_API_KEY, GC_API_URL (bound to that workspace), GC_LISTEN_SINCE (the old cursor:',
    'run `gc changes --since "$GC_LISTEN_SINCE"` to see the same items), GC_LISTEN_COUNT, GC_LISTEN_CHANGES_FILE',
    '(the /changes response as JSON). A failing command is logged; the cursor still advances.',
    'Without --exec: one line per item on stdout (JSON lines with --json). Logs go to stderr.',
  ].join('\n'),
  examples: [
    'gc listen --once --all-profiles --exec "openclaw cron run gc-worker-spawn"',
    'gc listen --interval 2m --exec \'claude -p "Run gc changes --since $GC_LISTEN_SINCE and handle it"\'',
    'gc listen --json',
  ],
  async run(ctx) {
    if (str(ctx.values, 'field')) throw new UsageError('--field does not apply to gc listen; use --json for JSON lines.')
    const interval = parseInterval(str(ctx.values, 'interval') ?? '60s')
    const once = ctx.values.once === true
    const exec = str(ctx.values, 'exec')
    const sinceFlag = str(ctx.values, 'since')
    const firstSince = sinceFlag ? parseSince(sinceFlag) : undefined
    const targets = resolveTargets(ctx)
    const env = ctx.sys.env

    const log = (target: string | null, msg: string) => ctx.sys.stderr(`${new Date().toISOString()} ${target ? `[${target}] ` : ''}${msg}\n`)

    let stopping = false
    let wake: (() => void) | null = null
    let child: ReturnType<typeof spawn> | null = null
    const offSignal = ctx.sys.onSignal?.((sig) => {
      if (stopping) return
      stopping = true
      log(null, `${sig} — stopping${child ? ' after the running command' : ''}`)
      child?.kill('SIGTERM')
      wake?.()
    })

    const pollOne = async (t: Target, sinceOverride: string | undefined): Promise<PollResult> => {
      const path = cursorPath(env, t.name)
      const since = sinceOverride ?? readCursor(path) ?? new Date().toISOString()
      let res: any
      try {
        res = await new GroundControlClient({ apiUrl: t.apiUrl, apiKey: t.apiKey }).getChanges(since)
      } catch (e) {
        if (isUnauthorized(e)) {
          log(t.name, `API key rejected (401) — reconnect with: gc onboarding${t.isProfile ? ` --profile ${t.name}` : ''} --token "gc_live_…"`)
          return 'unauthorized'
        }
        log(t.name, `poll failed: ${(e as Error).message}`)
        return 'error'
      }
      const next = serverCursor(res)
      if (!next) {
        log(t.name, 'the server sent no cursor (meta.cursor) — not advancing')
        return 'error'
      }
      const items = collectItems(t.name, res?.data)
      if (items.length === 0) {
        writeCursor(path, next)
        log(t.name, '0 changes')
        return 'ok'
      }
      const byKind = Object.entries(items.reduce<Record<string, number>>((m, i) => ({ ...m, [i.kind]: (m[i.kind] ?? 0) + 1 }), {}))
        .map(([k, n]) => `${k} ${n}`)
        .join(', ')

      if (!exec) {
        for (const i of items) ctx.out.line(ctx.values.json === true ? JSON.stringify(i) : itemLine(i))
        writeCursor(path, next)
        log(t.name, `${items.length} changes (${byKind})`)
        return 'ok'
      }

      const tmp = mkdtempSync(join(tmpdir(), 'gc-listen-'))
      const changesFile = join(tmp, 'changes.json')
      writeFileSync(changesFile, JSON.stringify(res, null, 2), { mode: 0o600 })
      const childEnv: Record<string, string | undefined> = {
        ...env,
        GC_API_KEY: t.apiKey,
        GC_API_URL: t.apiUrl,
        GC_LISTEN_SINCE: since,
        GC_LISTEN_COUNT: String(items.length),
        GC_LISTEN_CHANGES_FILE: changesFile,
      }
      // Only name a profile that exists — a GC_PROFILE the session's gc can't find is an error there.
      if (t.isProfile) childEnv.GC_PROFILE = t.name
      else delete childEnv.GC_PROFILE
      try {
        const code = await new Promise<number | null>((resolve, reject) => {
          const c = spawn('sh', ['-c', exec], { env: childEnv as NodeJS.ProcessEnv, stdio: ['ignore', 'inherit', 'inherit'] })
          child = c
          c.once('error', reject)
          c.once('spawn', () => {
            // Started: these items are handed over. Advance now, so a listener
            // killed while the session runs doesn't hand them over twice.
            writeCursor(path, next)
            log(t.name, `${items.length} changes (${byKind}) → exec (pid ${c.pid})`)
          })
          c.once('exit', (code, signal) => resolve(signal ? 128 : code))
        })
        if (code === 0) log(t.name, 'exec finished (exit 0)')
        else log(t.name, `exec exited with ${code} — cursor advanced anyway, these changes are not retried`)
        return 'ok'
      } catch (e) {
        log(t.name, `exec could not start: ${(e as Error).message} — cursor not advanced`)
        return 'error'
      } finally {
        child = null
        rmSync(tmp, { recursive: true, force: true })
      }
    }

    let failures = 0
    // --since holds per workspace until that workspace's poll succeeded; a
    // failed first poll must not drop the requested backfill window.
    const pendingSince = new Set(firstSince ? targets.map((t) => t.name) : [])
    try {
      while (!stopping) {
        const results: PollResult[] = []
        for (const t of targets) {
          if (stopping) break
          const r = await pollOne(t, pendingSince.has(t.name) ? firstSince : undefined)
          if (r === 'ok') pendingSince.delete(t.name)
          results.push(r)
        }
        if (results.length === targets.length && results.every((r) => r === 'unauthorized')) {
          throw new NotConnectedError(REJECTED, targets[0].apiUrl)
        }
        if (once || stopping) {
          const failed = targets.filter((_, i) => results[i] === 'error').map((t) => t.name)
          if (failed.length) throw new CliError(`Poll failed for: ${failed.join(', ')}`)
          return
        }
        failures = results.includes('error') ? failures + 1 : 0
        const delay = failures ? Math.min(interval * 2 ** failures, Math.max(interval, MAX_BACKOFF_MS)) : interval
        if (failures) log(null, `retrying in ${Math.round(delay / 1000)}s`)
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, delay)
          wake = () => {
            clearTimeout(timer)
            resolve()
          }
        })
        wake = null
      }
    } finally {
      offSignal?.()
    }
  },
}
