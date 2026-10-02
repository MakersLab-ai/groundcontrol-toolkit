import { CliError } from './errors'

export interface IO {
  stdout: (s: string) => void
  stderr: (s: string) => void
}

export interface OutputMode {
  json?: boolean
  field?: string
}

const MISSING = Symbol('missing')

/**
 * `--field a.b.0.c`. Looked up from the root of the API response first; when
 * that misses and the response has a `data` envelope, from inside it — so both
 * `--field data.title` and `--field title` work.
 */
export function extractField(root: unknown, path: string): unknown {
  const walk = (start: unknown): unknown => {
    let cur: unknown = start
    for (const part of path.split('.').filter(Boolean)) {
      if (cur === null || cur === undefined) return MISSING
      if (Array.isArray(cur)) {
        const i = Number(part)
        if (!Number.isInteger(i)) return MISSING
        cur = cur[i < 0 ? cur.length + i : i]
      } else if (typeof cur === 'object') {
        if (!(part in (cur as object))) return MISSING
        cur = (cur as Record<string, unknown>)[part]
      } else {
        return MISSING
      }
      if (cur === undefined) return MISSING
    }
    return cur
  }
  let v = walk(root)
  if (v === MISSING && root && typeof root === 'object' && 'data' in (root as object) && !path.startsWith('data.')) {
    v = walk((root as Record<string, unknown>).data)
  }
  if (v === MISSING) throw new CliError(`Field "${path}" not found in the response.`, 'Run the same command with --json to see the shape.')
  return v
}

export function formatField(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v, null, 2)
}

export class Output {
  constructor(readonly mode: OutputMode, readonly io: IO) {}

  get raw(): boolean {
    return Boolean(this.mode.json || this.mode.field)
  }

  /** Print an API result: raw JSON (`--json`), one value (`--field`), or the compact text. */
  emit(result: unknown, text: () => string | string[]): void {
    if (this.mode.field) {
      this.warnings(result)
      this.line(formatField(extractField(result, this.mode.field)))
      return
    }
    if (this.mode.json) {
      this.line(JSON.stringify(result, null, 2))
      return
    }
    this.warnings(result)
    const t = text()
    const s = Array.isArray(t) ? t.join('\n') : t
    if (s !== '') this.line(s)
  }

  line(s: string): void {
    this.io.stdout(s.endsWith('\n') ? s : s + '\n')
  }

  err(s: string): void {
    this.io.stderr(s.endsWith('\n') ? s : s + '\n')
  }

  /** Write routes return non-fatal `warnings` (e.g. `scrum_expects_review`) — never swallow them. */
  warnings(result: unknown): void {
    const w = (result as { warnings?: { code?: string; message?: string }[] } | null)?.warnings
    if (!Array.isArray(w)) return
    for (const item of w) this.err(`warning: ${item.code ?? ''}${item.message ? ` — ${item.message}` : ''}`)
  }
}

// ─── formatting helpers ───────────────────────────────────────────────────────

/** `2026-10-02 08:15Z` — UTC, minute precision, unambiguous for agents. */
export function fmtTime(iso: unknown): string {
  if (typeof iso !== 'string' || !iso) return '-'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return String(iso)
  return d.toISOString().slice(0, 16).replace('T', ' ') + 'Z'
}

export function oneLine(text: unknown, max = 160): string {
  const s = String(text ?? '').replace(/\s+/g, ' ').trim()
  return s.length > max ? s.slice(0, max - 1) + '…' : s
}

export function pad(s: unknown, n: number): string {
  const str = String(s ?? '-')
  return str.length >= n ? str : str + ' '.repeat(n - str.length)
}

type Any = Record<string, any>

export function name(m: Any | null | undefined): string | null {
  return m?.display_name ?? m?.name ?? null
}

/** One line per task: id, status, priority, title, assignee, initiative. */
export function taskLine(t: Any): string {
  const who = name(t.assignee)
  const parts = [t.id, pad(t.status, 11), pad(t.priority, 8), t.title]
  if (t.story_points !== null && t.story_points !== undefined) parts.push(`[${t.story_points}sp]`)
  if (who) parts.push(`@${who}`)
  if (t.initiative?.name) parts.push(`#${t.initiative.name}`)
  if (t.for === 'principal') parts.unshift('[principal]')
  return parts.join('  ')
}

/** "showing 20 of 154" when a list is truncated — a truncated list must say so. */
export function pageFooter(shown: number, meta: Any | undefined, hint: string): string | null {
  const total = meta?.total
  if (typeof total !== 'number' || total <= shown) return null
  const offset = typeof meta?.offset === 'number' ? meta.offset : 0
  return `-- showing ${offset + 1}–${offset + shown} of ${total}. ${hint.replace('{next}', String(offset + shown))}`
}

export function commentBlock(c: Any): string {
  const who = name(c.author) ?? c.author_id ?? 'Unknown'
  const kind = c.event_type && c.event_type !== 'comment' ? ` · ${c.event_type}` : ''
  return `--- ${who} · ${fmtTime(c.created_at)}${kind}\n${String(c.body ?? c.content ?? '').trimEnd()}`
}

export function humanSize(bytes: unknown): string {
  const n = Number(bytes)
  if (!Number.isFinite(n)) return '?'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
