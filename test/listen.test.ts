// `gc listen` end-to-end: the real entry point against a mock with two workspaces.
import { afterAll, beforeAll, beforeEach, describe, expect, test } from 'bun:test'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { collectItems, parseInterval } from '../src/commands/listen'

const KEY_A = 'gc_live_listenWorkspaceA0001'
const KEY_B = 'gc_live_listenWorkspaceB0002'
const CURSORS: Record<string, string> = { [KEY_A]: '2026-10-02T12:00:00.000Z', [KEY_B]: '2026-10-02T12:30:00.000Z' }
const state = { items: { [KEY_A]: true, [KEY_B]: false } as Record<string, boolean> }
const polls: { key: string; since: string }[] = []

let server: ReturnType<typeof Bun.serve>
let root: string
let dir: string
let base: string

beforeAll(() => {
  root = mkdtempSync(join(tmpdir(), 'gc-listen-'))
  server = Bun.serve({
    port: 0,
    fetch(req) {
      const url = new URL(req.url)
      const key = (req.headers.get('authorization') ?? '').replace(/^Bearer /, '')
      if (!CURSORS[key]) return Response.json({ error: { code: 'unauthorized', message: 'Invalid API key' } }, { status: 401 })
      if (url.pathname !== '/api/v1/changes') return Response.json({ error: { message: 'Not found' } }, { status: 404 })
      polls.push({ key, since: url.searchParams.get('since') ?? '' })
      const data = state.items[key]
        ? {
            task_comments: [{ id: 'c1', task_id: 't1', task_title: 'Ship', content: 'hi', created_at: '2026-10-02T11:59:00Z', for: 'self' }],
            tasks_updated: [{ id: 't2', title: 'Book flights', updated_at: '2026-10-02T11:58:00Z', for: 'principal' }],
            some_future_kind: [{ id: 'f1', created_at: '2026-10-02T11:57:00Z' }],
            goal_checkins: [],
          }
        : { task_comments: [], tasks_updated: [], goal_checkins: [] }
      return Response.json({ data, meta: { since: url.searchParams.get('since'), cursor: CURSORS[key], checked_at: '2026-10-02T13:00:00.000Z' } })
    },
  })
  base = `http://127.0.0.1:${server.port}/api/v1`
})

afterAll(() => {
  server.stop(true)
  rmSync(root, { recursive: true, force: true })
})

beforeEach(() => {
  dir = mkdtempSync(join(root, 'case-'))
  mkdirSync(join(dir, 'config'), { recursive: true })
  const profile = (key: string) => ({ api_url: base, api_key: key, workspace: key.slice(-5), agent: 'Robo', created_at: '2026-10-01T00:00:00Z' })
  writeFileSync(join(dir, 'config', 'config.json'), JSON.stringify({ current: 'alpha', profiles: { alpha: profile(KEY_A), beta: profile(KEY_B) } }), { mode: 0o600 })
  polls.length = 0
  state.items = { [KEY_A]: true, [KEY_B]: false }
})

function spawnGc(args: string[], env: Record<string, string> = {}) {
  return Bun.spawn([process.execPath, 'run', join(import.meta.dir, '..', 'src', 'main.ts'), ...args], {
    env: { PATH: process.env.PATH ?? '', HOME: dir, GC_CONFIG_DIR: join(dir, 'config'), ...env },
    stdin: 'ignore',
    stdout: 'pipe',
    stderr: 'pipe',
  })
}

async function gc(args: string[], env: Record<string, string> = {}) {
  const proc = spawnGc(args, env)
  const [out, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited])
  return { out, err, code }
}

const cursorOf = (name: string) => JSON.parse(readFileSync(join(dir, 'config', 'listen', `${name}.json`), 'utf8')).cursor

describe('gc listen', () => {
  test('pure helpers', () => {
    expect(parseInterval('60s')).toBe(60_000)
    expect(parseInterval('5m')).toBe(300_000)
    expect(parseInterval('30')).toBe(30_000)
    expect(() => parseInterval('1s')).toThrow(/at least 5s/)
    expect(() => parseInterval('soon')).toThrow(/Invalid --interval/)
    const items = collectItems('p', { a: [{ id: 1 }], b: [], c: 'not a list', d: [{ id: 2, for: 'principal' }, { id: 3 }] })
    expect(items.map((i) => `${i.kind}:${i.id}:${i.for}`)).toEqual(['a:1:self', 'd:2:principal', 'd:3:self'])
  })

  test('--once: first run starts at now, prints JSON lines for every array, cursor = meta.cursor; next run resumes there', async () => {
    const before = Date.now()
    let r = await gc(['listen', '--once', '--json'])
    expect(r.code).toBe(0)
    expect(Math.abs(Date.parse(polls[0].since) - before)).toBeLessThan(10_000)
    const lines = r.out.trim().split('\n').map((l) => JSON.parse(l))
    expect(lines.map((l) => l.kind)).toEqual(['task_comments', 'tasks_updated', 'some_future_kind'])
    expect(lines[0]).toMatchObject({ profile: 'alpha', kind: 'task_comments', for: 'self', id: 'c1', task_id: 't1', created_at: '2026-10-02T11:59:00Z' })
    expect(lines[1].for).toBe('principal')
    expect(r.err).toContain('[alpha] 3 changes (task_comments 1, tasks_updated 1, some_future_kind 1)')
    expect(cursorOf('alpha')).toBe(CURSORS[KEY_A])
    expect(statSync(join(dir, 'config', 'listen', 'alpha.json')).mode & 0o777).toBe(0o600)

    r = await gc(['listen', '--once'])
    expect(r.code).toBe(0)
    expect(polls[1].since).toBe(CURSORS[KEY_A])
    expect(r.out).toContain('alpha  task_comments  c1  task t1')
  })

  test('--since overrides the stored cursor', async () => {
    await gc(['listen', '--once', '--since', '2026-10-01T00:00:00Z'])
    expect(polls[0].since).toBe('2026-10-01T00:00:00.000Z')
  })

  test('--all-profiles: separate cursors per workspace', async () => {
    const r = await gc(['listen', '--once', '--all-profiles'])
    expect(r.code).toBe(0)
    expect(polls.map((p) => p.key)).toEqual([KEY_A, KEY_B])
    expect(cursorOf('alpha')).toBe(CURSORS[KEY_A])
    expect(cursorOf('beta')).toBe(CURSORS[KEY_B])
    expect(r.err).toContain('[beta] 0 changes')
    await gc(['listen', '--once', '--all-profiles'])
    expect(polls[2]).toEqual({ key: KEY_A, since: CURSORS[KEY_A] })
    expect(polls[3]).toEqual({ key: KEY_B, since: CURSORS[KEY_B] })
  })

  test('--exec: runs only when there are changes, with the workspace env; non-zero exit still advances', async () => {
    const envOut = join(dir, 'env.txt')
    const cmd = `printf '%s\\n' "$GC_PROFILE" "$GC_API_KEY" "$GC_API_URL" "$GC_LISTEN_SINCE" "$GC_LISTEN_COUNT" > ${envOut}; cat "$GC_LISTEN_CHANGES_FILE" >> ${envOut}; echo "ran $GC_PROFILE" >> ${join(dir, 'runs.txt')}; exit 7`
    const r = await gc(['listen', '--once', '--all-profiles', '--since', '2026-10-02T10:00:00Z', '--exec', cmd])
    expect(r.code).toBe(0)
    const [profile, key, url, since, count, ...json] = readFileSync(envOut, 'utf8').split('\n')
    expect([profile, key, url, since, count]).toEqual(['alpha', KEY_A, base, '2026-10-02T10:00:00.000Z', '3'])
    expect(JSON.parse(json.join('\n')).meta.cursor).toBe(CURSORS[KEY_A])
    // beta had no changes → no exec
    expect(readFileSync(join(dir, 'runs.txt'), 'utf8')).toBe('ran alpha\n')
    expect(r.err).toContain('exec exited with 7 — cursor advanced anyway')
    expect(cursorOf('alpha')).toBe(CURSORS[KEY_A])
    expect(r.err).not.toContain(KEY_A)
  })

  test('a key from GC_API_KEY gets a hashed cursor name and no GC_PROFILE in the exec env', async () => {
    const out = join(dir, 'p.txt')
    const r = await gc(['listen', '--once', '--exec', `printf '[%s]' "\${GC_PROFILE-unset}" > ${out}`], { GC_API_KEY: KEY_A, GC_API_URL: base, GC_PROFILE: 'alpha' })
    expect(r.code).toBe(0)
    expect(readFileSync(out, 'utf8')).toBe('[unset]')
    expect(r.err).toMatch(/\[key-[0-9a-f]{12}\]/)
  })

  test('401: one rejected profile is logged and the others continue; all rejected → exit 3', async () => {
    const cfgPath = join(dir, 'config', 'config.json')
    const cfg = JSON.parse(readFileSync(cfgPath, 'utf8'))
    cfg.profiles.beta.api_key = 'gc_live_revokedrevoked0000'
    writeFileSync(cfgPath, JSON.stringify(cfg))
    let r = await gc(['listen', '--once', '--all-profiles'])
    expect(r.code).toBe(0)
    expect(r.err).toContain('[beta] API key rejected (401)')
    expect(cursorOf('alpha')).toBe(CURSORS[KEY_A])

    cfg.profiles.alpha.api_key = 'gc_live_revokedrevoked1111'
    writeFileSync(cfgPath, JSON.stringify(cfg))
    r = await gc(['listen', '--once', '--all-profiles'])
    expect(r.code).toBe(3)
    expect(r.err).toContain('GROUNDCONTROL is not connected on this machine (the API key was rejected: 401)')
  })

  test('no key at all → exit 3', async () => {
    writeFileSync(join(dir, 'config', 'config.json'), JSON.stringify({ current: null, profiles: {} }))
    const r = await gc(['listen', '--once'])
    expect(r.code).toBe(3)
    expect(r.err).toContain('(no API key)')
  })

  test('loop mode stops cleanly on SIGTERM', async () => {
    state.items[KEY_A] = false
    const proc = spawnGc(['listen', '--interval', '5s'])
    const deadline = Date.now() + 5_000
    while (polls.length === 0 && Date.now() < deadline) await Bun.sleep(50)
    expect(polls.length).toBe(1)
    proc.kill('SIGTERM')
    const code = await proc.exited
    expect(code).toBe(0)
    expect(await new Response(proc.stderr).text()).toContain('SIGTERM — stopping')
    expect(existsSync(join(dir, 'config', 'listen', 'alpha.json'))).toBe(true)
  })
})
