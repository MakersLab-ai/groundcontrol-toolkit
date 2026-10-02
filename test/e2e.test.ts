// End-to-end: the real CLI entry point (src/main.ts, a separate process)
// against a tiny mock of the GROUNDCONTROL API.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, existsSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const KEY = 'gc_live_e2eSecretKey0000abcd'
const TASK = '11111111-2222-3333-4444-555555555555'
const CHECKED_AT = '2026-10-02T12:00:05.000Z'
const CURSOR = '2026-10-02T12:00:00.000Z'
const mock = { cursor: CURSOR as string | undefined, changes: true }

type Req = { method: string; path: string; search: URLSearchParams; auth: string | null; session: string | null; body: any }
const requests: Req[] = []
let server: ReturnType<typeof Bun.serve>
let dir: string
let base: string

const json = (data: unknown, status = 200) => Response.json(data, { status })

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'gc-e2e-'))
  server = Bun.serve({
    port: 0,
    async fetch(req) {
      const url = new URL(req.url)
      const body = req.method === 'POST' || req.method === 'PATCH' ? await req.json().catch(() => null) : null
      requests.push({ method: req.method, path: url.pathname, search: url.searchParams, auth: req.headers.get('authorization'), session: req.headers.get('x-gc-session-id'), body })
      const p = url.pathname.replace(/^\/api\/v1/, '')
      if (p === '/guide' && req.method === 'GET') return json({ data: [{ topic: 'start', title: 'Start here', summary: 'First steps' }] })
      if (p === '/guide/tasks') return json({ data: { topic: 'tasks', title: 'Tasks', body: '# Tasks\n\nUse gc tasks.' } })
      if (req.headers.get('authorization') !== `Bearer ${KEY}`) return json({ error: { code: 'unauthorized', message: 'Invalid API key' } }, 401)
      if (p === '/me') {
        return json({ data: { user: { id: 'm1', display_name: 'Robo', is_agent: true, role: 'member' }, tenant: { id: 't1', name: 'Acme Labs', slug: 'acme', workflow: 'scrum', features: { datasheets: true, desk: false } }, tenants: [] } })
      }
      if (p === '/tasks' && req.method === 'GET') {
        return json({ data: [{ id: TASK, title: 'Ship the CLI', status: 'in_progress', priority: 'high', assignee: { display_name: 'Robo' }, initiative: { name: 'Core' } }], meta: { total: 1, limit: 100, offset: 0 } })
      }
      if (p === '/tasks' && req.method === 'POST') return json({ data: { id: TASK, status: 'todo', priority: 'medium', ...body } }, 201)
      if (p === `/tasks/${TASK}` && req.method === 'PATCH') {
        return json({ data: { id: TASK, title: 'Ship the CLI', status: 'review', priority: 'high', ...body }, warnings: body?.status === 'done' ? [{ code: 'scrum_expects_review', message: 'finish with review' }] : undefined })
      }
      if (p === `/tasks/${TASK}`) {
        return json({ data: { id: TASK, title: 'Ship the CLI', status: 'in_progress', priority: 'high', description: 'Build **gc**.', assignee: { display_name: 'Robo' }, initiative: { name: 'Core' }, updated_at: '2026-10-02T09:00:00Z' } })
      }
      if (p === `/tasks/${TASK}/comments` && req.method === 'GET') {
        return json({ data: [
          { id: 'c1', author: { display_name: 'Chris' }, body: 'Old comment', created_at: '2026-10-01T08:00:00Z' },
          { id: 'c2', author: { display_name: 'Chris' }, body: 'New comment', created_at: '2026-10-02T11:00:00Z' },
        ], meta: { total: 2 } })
      }
      if (p === `/tasks/${TASK}/comments` && req.method === 'POST') return json({ data: { id: 'c3', body: body?.body } }, 201)
      if (p === `/tasks/${TASK}/attachments`) return json({ data: [] })
      if (p === '/changes') {
        if (!mock.changes) return json({ data: { task_comments: [], tasks_updated: [], goal_checkins: [] }, meta: { since: url.searchParams.get('since'), cursor: mock.cursor, checked_at: CHECKED_AT } })
        return json({ data: {
          task_comments: [{ id: 'c2', task_id: TASK, content: 'Please also add docs', created_at: '2026-10-02T11:00:00Z', task_title: 'Ship the CLI', for: 'self' }],
          doc_comments: [], tasks_created: [], docs_updated: [], goal_checkins: [],
          tasks_updated: [{ id: 'x2', title: 'Book flights', status: 'todo', priority: 'medium', for: 'principal' }],
        }, meta: { since: url.searchParams.get('since'), cursor: mock.cursor, checked_at: CHECKED_AT } })
      }
      return json({ error: { code: 'not_found', message: 'Not found' } }, 404)
    },
  })
  base = `http://127.0.0.1:${server.port}/api/v1`
})

afterAll(() => {
  server.stop(true)
  rmSync(dir, { recursive: true, force: true })
})

async function gc(args: string[], { stdin, env = {} }: { stdin?: string; env?: Record<string, string> } = {}) {
  const proc = Bun.spawn([process.execPath, 'run', join(import.meta.dir, '..', 'src', 'main.ts'), ...args], {
    env: { PATH: process.env.PATH ?? '', HOME: dir, GC_CONFIG_DIR: join(dir, 'config'), ...env },
    stdin: stdin === undefined ? 'ignore' : new TextEncoder().encode(stdin),
    stdout: 'pipe',
    stderr: 'pipe',
  })
  const [out, err, code] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited])
  return { out, err, code }
}

describe('gc against a mock API', () => {
  test('onboarding reads the key from stdin, verifies it, stores it 0600, never prints it', async () => {
    const r = await gc(['onboarding', '--token', '-', '--api-url', base], { stdin: KEY + '\n' })
    expect(r.err).toBe('')
    expect(r.code).toBe(0)
    expect(r.out).toContain('Connected to workspace "Acme Labs" as "Robo" (scrum)')
    expect(r.out).toContain('Profile: acme-labs (current)')
    expect(r.out + r.err).not.toContain(KEY)
    expect(r.out).toContain('gc_live_…abcd')
    const cfgPath = join(dir, 'config', 'config.json')
    expect(statSync(cfgPath).mode & 0o777).toBe(0o600)
    const cfg = JSON.parse(readFileSync(cfgPath, 'utf8'))
    expect(cfg.current).toBe('acme-labs')
    expect(cfg.profiles['acme-labs']).toMatchObject({ api_url: base, api_key: KEY, workspace: 'Acme Labs', agent: 'Robo' })
    expect(requests.at(-1)?.path).toBe('/api/v1/me')
  })

  test('onboarding with a bad key fails and saves nothing new', async () => {
    const r = await gc(['onboarding', '--token', 'gc_live_wrongwrongwrong', '--api-url', base, '--profile', 'bad'])
    expect(r.code).toBe(3)
    expect(r.err).toContain('401')
    expect(r.err).not.toContain('gc_live_wrongwrongwrong')
    expect(JSON.parse(readFileSync(join(dir, 'config', 'config.json'), 'utf8')).profiles.bad).toBeUndefined()
  })

  test('context: identity, workflow rule, open tasks; session id header', async () => {
    const r = await gc(['context'], { env: { GC_SESSION_ID: 'sess-42' } })
    expect(r.code).toBe(0)
    expect(r.out).toContain('Robo (agent, member) in workspace "Acme Labs" · profile acme-labs')
    expect(r.out).toContain('scrum — finish your tasks with status review')
    expect(r.out).toContain(`${TASK}  in_progress  high      Ship the CLI  @Robo  #Core`)
    const list = requests.find((q) => q.path === '/api/v1/tasks')!
    expect(list.search.get('assigned_to')).toBe('me')
    expect(list.search.get('status')).toBe('backlog,scheduled,todo,in_progress,blocked,review')
    expect(list.session).toBe('sess-42')
  })

  test('tasks get: description + comments; --since only new; --field', async () => {
    let r = await gc(['tasks', 'get', TASK])
    expect(r.code).toBe(0)
    expect(r.out).toContain('# Ship the CLI')
    expect(r.out).toContain('Build **gc**.')
    expect(r.out).toContain('## Comments (2)')
    expect(r.out).toContain('--- Chris · 2026-10-01 08:00Z\nOld comment')
    r = await gc(['tasks', 'get', TASK, '--since', '2026-10-02T00:00:00Z'])
    expect(r.out).toContain('## Comments (1 new since 2026-10-02 00:00Z, 2 total)')
    expect(r.out).not.toContain('Old comment')
    r = await gc(['tasks', 'get', TASK, '--field', 'description'])
    expect(r.out).toBe('Build **gc**.\n')
    r = await gc(['tasks', 'get', TASK, '--json'])
    expect(JSON.parse(r.out).data.comments).toHaveLength(2)
  })

  test('comment --body-file - posts stdin Markdown verbatim', async () => {
    const md = '## Done\n\n- PR: https://example.com/pr/1\n- `code`\n'
    const r = await gc(['comment', TASK, '--body-file', '-'], { stdin: md })
    expect(r.code).toBe(0)
    expect(r.out).toContain(`commented on ${TASK} (c3)`)
    const post = requests.filter((q) => q.method === 'POST').at(-1)!
    expect(post.body).toEqual({ body: md.trimEnd() })
  })

  test('changes --cursor-file: missing file → 1h default; writes the server cursor; reuses it', async () => {
    const cursor = join(dir, 'state', 'cursor')
    const before = Date.now()
    let r = await gc(['changes', '--cursor-file', cursor])
    expect(r.code).toBe(0)
    expect(r.err).toContain('does not exist yet')
    expect(r.out).toContain('Please also add docs')
    expect(r.out).toContain('[principal]')
    expect(r.out).toContain('do NOT start it')
    const since1 = Date.parse(requests.filter((q) => q.path === '/api/v1/changes').at(-1)!.search.get('since')!)
    expect(Math.abs(since1 - (before - 3_600_000))).toBeLessThan(10_000)
    expect(readFileSync(cursor, 'utf8').trim()).toBe(CURSOR) // meta.cursor, not checked_at, not the local clock
    expect(statSync(cursor).mode & 0o777).toBe(0o600)

    r = await gc(['changes', '--cursor-file', cursor])
    expect(r.code).toBe(0)
    expect(requests.filter((q) => q.path === '/api/v1/changes').at(-1)!.search.get('since')).toBe(CURSOR)

    // older server without meta.cursor → checked_at
    mock.cursor = undefined
    r = await gc(['changes', '--cursor-file', cursor])
    mock.cursor = CURSOR
    expect(readFileSync(cursor, 'utf8').trim()).toBe(CHECKED_AT)

    // --since wins over the file
    r = await gc(['changes', '--since', '2026-10-02T06:00:00Z', '--cursor-file', cursor])
    expect(requests.filter((q) => q.path === '/api/v1/changes').at(-1)!.search.get('since')).toBe('2026-10-02T06:00:00.000Z')
  })

  test('tasks create/update: empty flags are not sent, "none" clears, warnings go to stderr', async () => {
    let r = await gc(['tasks', 'create', '--title', 'New one', '--priority', '', '--description-file', '-', '--story-points', '3'], { stdin: 'Desc **md**\n' })
    expect(r.code).toBe(0)
    expect(requests.at(-1)!.body).toEqual({ title: 'New one', description: 'Desc **md**\n', story_points: 3 })
    r = await gc(['tasks', 'update', TASK, '--assign', 'none', '--status', 'done', '--due', ''])
    expect(r.code).toBe(0)
    expect(requests.at(-1)!.body).toEqual({ assigned_to: null, status: 'done' })
    expect(r.err).toContain('warning: scrum_expects_review')
    r = await gc(['tasks', 'update', TASK])
    expect(r.code).toBe(2)
    r = await gc(['tasks', 'create', '--story-points', 'many', '--title', 'x'])
    expect(r.code).toBe(2)
  })

  test('changes --cursor-file: output fails (bad --field) → cursor not advanced', async () => {
    const cursor = join(dir, 'state', 'cursor-keep')
    mkdirSync(join(dir, 'state'), { recursive: true })
    writeFileSync(cursor, '2026-10-01T00:00:00.000Z\n')
    const r = await gc(['changes', '--cursor-file', cursor, '--field', 'data.no_such_list'])
    expect(r.code).not.toBe(0)
    expect(readFileSync(cursor, 'utf8').trim()).toBe('2026-10-01T00:00:00.000Z')
  })

  test('changes without cursor file stores nothing anywhere', async () => {
    const r = await gc(['changes', '--since', '15m'])
    expect(r.code).toBe(0)
    expect(existsSync(join(dir, 'config', 'cursor'))).toBe(false)
  })

  test('guide lists topics and prints a body raw', async () => {
    let r = await gc(['guide'])
    expect(r.out).toContain('start  Start here — First steps')
    r = await gc(['guide', 'tasks'])
    expect(r.out).toBe('# Tasks\n\nUse gc tasks.\n')
  })

  test('API errors exit 1; a rejected key exits 3 with the way to connect; GC_API_KEY overrides the profile', async () => {
    let r = await gc(['tasks', 'get', 'does-not-exist'])
    expect(r.code).toBe(1)
    expect(r.err).toContain('GROUNDCONTROL API error 404')
    r = await gc(['context'], { env: { GC_API_KEY: 'gc_live_overrideoverride' } })
    expect(r.code).toBe(3)
    expect(r.err).toContain('not connected on this machine (the API key was rejected: 401)')
    expect(r.err).toContain(`registers at http://127.0.0.1:${server.port} `)
    expect(r.err).not.toContain('gc_live_overrideoverride')
  })
})
