import { UsageError } from './errors'

const UNIT_MS: Record<string, number> = { s: 1_000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 }

/**
 * `--since` accepts an ISO timestamp or a relative age (`90s`, `15m`, `2h`,
 * `1d`, `1w`) and always returns a canonical ISO string — the server parses it
 * with `new Date()`, so we fail here, with a usable message, instead of there.
 */
export function parseSince(input: string, now: Date = new Date()): string {
  const value = input.trim()
  const rel = /^(\d+)\s*([smhdw])$/i.exec(value)
  if (rel) {
    const ms = Number(rel[1]) * UNIT_MS[rel[2].toLowerCase()]
    return new Date(now.getTime() - ms).toISOString()
  }
  // Only things that look like dates — `Date.parse('5')` is a valid year.
  if (/^\d{4}-\d{2}-\d{2}/.test(value)) {
    const t = Date.parse(value)
    if (!Number.isNaN(t)) return new Date(t).toISOString()
  }
  throw new UsageError(
    `Invalid --since value "${input}".`,
    'Use an ISO timestamp (2026-10-02T08:00:00Z) or an age: 90s, 15m, 2h, 1d, 1w.',
  )
}
