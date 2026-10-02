// Exit codes: 0 ok · 1 API/other failure · 2 usage mistake (unknown flag,
// missing argument) · 3 not connected (no API key, or the key was rejected).
// A caller — usually an agent — can tell "I called it wrong" from "the call
// failed" from "this machine isn't set up", and only the last one needs a human.
export class CliError extends Error {
  constructor(message: string, readonly hint?: string, readonly exitCode = 1) {
    super(message)
  }
}

export class UsageError extends CliError {
  constructor(message: string, hint?: string) {
    super(message, hint, 2)
  }
}

export const DEFAULT_ORIGIN = 'https://groundcontrol.makerslab.ai'

export function originOf(apiUrl: string | undefined): string {
  try {
    if (apiUrl) return new URL(apiUrl).origin
  } catch { /* not a URL — fall through */ }
  return DEFAULT_ORIGIN
}

/** The way back for an agent that finds gc unconnected: written for the agent to relay to its human. */
export function notConnectedText(reason: string, apiUrl?: string): string {
  const origin = originOf(apiUrl)
  return [
    `GROUNDCONTROL is not connected on this machine (${reason}).`,
    'To connect:',
    `  1. Your human registers at ${origin} (or opens Settings → API Keys there if the workspace exists).`,
    '  2. After registration the setup screen (/agent ready_) shows a prompt with the key; or create a key under Settings → API Keys.',
    '  3. Run: gc onboarding --token "gc_live_…"',
    'Then check with: gc context',
  ].join('\n')
}

export class NotConnectedError extends CliError {
  constructor(reason: string, apiUrl?: string) {
    super(notConnectedText(reason, apiUrl), undefined, 3)
  }
}

export const REJECTED = 'the API key was rejected: 401'

/** The shared client and apiGet both throw `GROUNDCONTROL API error <status>: …`. */
export function isUnauthorized(e: unknown): boolean {
  return /API error 401\b/.test((e as Error)?.message ?? '')
}
