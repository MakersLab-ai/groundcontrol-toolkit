import { redactKey } from './redact'
import type { Ctx } from './command'
import { CliError } from './errors'

/**
 * GET on the API base for the few endpoints the shared client has no method
 * for (`/guide`, `/initiatives?with_counts=true`). Same error shape as the
 * client: `GROUNDCONTROL API error <status>: <message>`, keys redacted.
 * `auth: 'optional'` sends the key when there is one and works without.
 */
export async function apiGet(ctx: Ctx, path: string, { auth = 'required' }: { auth?: 'required' | 'optional' } = {}): Promise<any> {
  const a = ctx.auth()
  if (auth === 'required' && !a.apiKey) ctx.client() // throws the "not connected" error
  const sessionId = (typeof ctx.values['session-id'] === 'string' && ctx.values['session-id']) || ctx.sys.env.GC_SESSION_ID
  let res: Response
  try {
    res = await fetch(`${a.apiUrl}${path}`, {
      headers: {
        ...(a.apiKey ? { Authorization: `Bearer ${a.apiKey}` } : {}),
        ...(sessionId ? { 'X-GC-Session-Id': sessionId } : {}),
      },
    })
  } catch (e) {
    throw new CliError(`Cannot reach ${a.apiUrl}: ${(e as Error).message}`)
  }
  if (!res.ok) {
    const body: any = await res.json().catch(() => ({}))
    throw new CliError(`GROUNDCONTROL API error ${res.status}: ${redactKey(body?.error?.message || res.statusText)}`)
  }
  return res.json()
}
