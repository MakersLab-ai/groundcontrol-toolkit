import { describe, expect, test, beforeEach, afterEach } from 'bun:test'
import { mkdtempSync, readFileSync, rmSync, statSync, writeFileSync, readdirSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { configPath, loadConfig, redactKey, resolveAuth, saveConfig, slugify, DEFAULT_API_URL, type ConfigFile } from '../src/config'
import { parseSince } from '../src/since'
import { extractField, formatField, taskLine, pageFooter, commentBlock, fmtTime } from '../src/output'
import { parseCommandArgs, resolveCommand, run, COMMANDS } from '../src/cli'
import { SKILLS } from '../src/skills'
import { parseKr } from '../src/commands/goals'
import { parseFieldSpec, rowLine } from '../src/commands/tables'
import { formatChanges } from '../src/commands/context'
import { UsageError } from '../src/errors'
import type { Sys } from '../src/command'

let dir: string
beforeEach(() => { dir = mkdtempSync(join(tmpdir(), 'gc-test-')) })
afterEach(() => { rmSync(dir, { recursive: true, force: true }) })

const profile = (key: string, url = 'https://a.example/api/v1') => ({ api_url: url, api_key: key, workspace: 'W', agent: 'A', created_at: '2026-10-01T00:00:00Z' })

describe('config', () => {
  test('dir precedence: GC_CONFIG_DIR > XDG_CONFIG_HOME > ~/.config', () => {
    expect(configPath({ GC_CONFIG_DIR: '/x', XDG_CONFIG_HOME: '/y', HOME: '/h' })).toBe('/x/config.json')
    expect(configPath({ XDG_CONFIG_HOME: '/y', HOME: '/h' })).toBe('/y/groundcontrol/config.json')
    expect(configPath({ HOME: '/h' })).toBe('/h/.config/groundcontrol/config.json')
  })

  test('save: dir 0700, file 0600, atomic (no tmp file left), round-trips', () => {
    const env = { GC_CONFIG_DIR: join(dir, 'cfg') }
    const cfg: ConfigFile = { current: 'a', profiles: { a: profile('gc_live_aaaaaaaaaaaa1111') } }
    const path = saveConfig(cfg, env)
    expect(statSync(join(dir, 'cfg')).mode & 0o777).toBe(0o700)
    expect(statSync(path).mode & 0o777).toBe(0o600)
    expect(readdirSync(join(dir, 'cfg'))).toEqual(['config.json'])
    expect(loadConfig(env)).toEqual(cfg)
    // a second save replaces the file and keeps the mode
    cfg.current = null
    saveConfig(cfg, env)
    expect(statSync(path).mode & 0o777).toBe(0o600)
    expect(loadConfig(env).current).toBeNull()
  })

  test('missing file → empty config; broken JSON → clear error', () => {
    const env = { GC_CONFIG_DIR: dir }
    expect(loadConfig(env)).toEqual({ current: null, profiles: {} })
    writeFileSync(join(dir, 'config.json'), '{nope')
    expect(() => loadConfig(env)).toThrow(/not valid JSON/)
  })

  test('key precedence: --token > GC_API_KEY > profile; url: flag > env > profile > default', () => {
    const cfg: ConfigFile = { current: 'a', profiles: { a: profile('gc_live_profileA0000'), b: profile('gc_live_profileB0000', 'https://b.example/api/v1/') } }
    expect(resolveAuth({}, {}, cfg)).toMatchObject({ apiKey: 'gc_live_profileA0000', keySource: 'profile', profileName: 'a', apiUrl: 'https://a.example/api/v1' })
    expect(resolveAuth({}, { GC_API_KEY: 'env' }, cfg)).toMatchObject({ apiKey: 'env', keySource: 'env' })
    expect(resolveAuth({ token: 'flag' }, { GC_API_KEY: 'env' }, cfg)).toMatchObject({ apiKey: 'flag', keySource: 'flag' })
    // profile selection: --profile > GC_PROFILE > current
    expect(resolveAuth({}, { GC_PROFILE: 'b' }, cfg).apiKey).toBe('gc_live_profileB0000')
    expect(resolveAuth({ profile: 'a' }, { GC_PROFILE: 'b' }, cfg).apiKey).toBe('gc_live_profileA0000')
    expect(resolveAuth({ profile: 'b' }, {}, cfg).apiUrl).toBe('https://b.example/api/v1')
    expect(resolveAuth({ profile: 'b' }, { GC_API_URL: 'http://env/api/v1' }, cfg).apiUrl).toBe('http://env/api/v1')
    expect(resolveAuth({ profile: 'b', 'api-url': 'http://flag' }, { GC_API_URL: 'http://env' }, cfg).apiUrl).toBe('http://flag')
    expect(resolveAuth({}, {}, { current: null, profiles: {} })).toMatchObject({ apiUrl: DEFAULT_API_URL, apiKey: undefined, keySource: 'none' })
  })

  test('an explicitly named unknown profile is an error, never a silent fallback', () => {
    const cfg: ConfigFile = { current: 'a', profiles: { a: profile('k') } }
    expect(() => resolveAuth({ profile: 'zzz' }, {}, cfg)).toThrow(/Unknown profile "zzz"/)
    expect(() => resolveAuth({}, { GC_PROFILE: 'zzz' }, cfg)).toThrow(/Unknown profile/)
  })

  test('redactKey shows at most the last 4 characters', () => {
    expect(redactKey('gc_live_abcdefghijklmnop1234')).toBe('gc_live_…1234')
    expect(redactKey('short')).toBe('…')
    expect(redactKey(undefined)).toBe('(none)')
  })

  test('slugify', () => {
    expect(slugify('MakersLab GmbH')).toBe('makerslab-gmbh')
    expect(slugify('Jörg’s Büro')).toBe('jorg-s-buro')
    expect(slugify('!!!')).toBe('default')
  })
})

describe('parseSince', () => {
  const now = new Date('2026-10-02T12:00:00.000Z')
  test('relative ages', () => {
    expect(parseSince('15m', now)).toBe('2026-10-02T11:45:00.000Z')
    expect(parseSince('2h', now)).toBe('2026-10-02T10:00:00.000Z')
    expect(parseSince('1d', now)).toBe('2026-10-01T12:00:00.000Z')
    expect(parseSince('90s', now)).toBe('2026-10-02T11:58:30.000Z')
    expect(parseSince('1w', now)).toBe('2026-09-25T12:00:00.000Z')
  })
  test('ISO timestamps are canonicalised', () => {
    expect(parseSince('2026-10-02T08:00:00Z', now)).toBe('2026-10-02T08:00:00.000Z')
    expect(parseSince('2026-10-02T10:00:00+02:00', now)).toBe('2026-10-02T08:00:00.000Z')
    expect(parseSince('2026-10-02', now)).toBe('2026-10-02T00:00:00.000Z')
  })
  test('garbage is a usage error', () => {
    for (const bad of ['5', 'yesterday', '15x', '', '2026-13-45']) expect(() => parseSince(bad, now)).toThrow(UsageError)
  })
})

describe('--field', () => {
  const res = { data: { id: 't1', title: 'Hello', tags: [{ n: 'a' }, { n: 'b' }], nested: { x: null } }, meta: { total: 3 } }
  test('paths from the root, through arrays, and relative to data', () => {
    expect(extractField(res, 'data.title')).toBe('Hello')
    expect(extractField(res, 'title')).toBe('Hello')
    expect(extractField(res, 'data.tags.1.n')).toBe('b')
    expect(extractField(res, 'tags.-1.n')).toBe('b')
    expect(extractField(res, 'meta.total')).toBe(3)
    expect(extractField(res, 'nested.x')).toBeNull()
  })
  test('missing path throws', () => {
    expect(() => extractField(res, 'data.nope')).toThrow(/not found/)
    expect(() => extractField(res, 'data.tags.x')).toThrow(/not found/)
  })
  test('strings raw, everything else JSON', () => {
    expect(formatField('a\nb')).toBe('a\nb')
    expect(formatField(3)).toBe('3')
    expect(formatField({ a: 1 })).toBe('{\n  "a": 1\n}')
    expect(formatField(null)).toBe('null')
  })
})

describe('output formatting', () => {
  test('task line: full id, status, priority, title, points, assignee, initiative', () => {
    const line = taskLine({ id: '11111111-2222-3333-4444-555555555555', status: 'in_progress', priority: 'high', title: 'Fix it', story_points: 3, assignee: { display_name: 'Ada' }, initiative: { name: 'Core' } })
    expect(line).toBe('11111111-2222-3333-4444-555555555555  in_progress  high      Fix it  [3sp]  @Ada  #Core')
    expect(taskLine({ id: 'x', status: 'todo', priority: 'low', title: 'T', for: 'principal' })).toStartWith('[principal]  x')
  })
  test('page footer only when truncated', () => {
    expect(pageFooter(20, { total: 20 }, 'x')).toBeNull()
    expect(pageFooter(20, { total: 154, offset: 0 }, 'Next page: --offset {next}')).toBe('-- showing 1–20 of 154. Next page: --offset 20')
  })
  test('comment block', () => {
    expect(commentBlock({ author: { display_name: 'Bo' }, created_at: '2026-10-02T08:15:30Z', body: 'hi\n' })).toBe('--- Bo · 2026-10-02 08:15Z\nhi')
    expect(fmtTime(undefined)).toBe('-')
  })
  test('changes: principal items are marked and explained', () => {
    const lines = formatChanges({ data: { task_comments: [{ task_id: 't', task_title: 'T', content: 'hello', created_at: '2026-10-02T08:00:00Z', for: 'principal' }], tasks_created: [], goal_checkins: [] } }, '2026-10-02T07:00:00.000Z')
    expect(lines.join('\n')).toContain('[principal] task t "T"')
    expect(lines.join('\n')).toContain('do NOT start it')
    expect(formatChanges({ data: {} }, '2026-10-02T07:00:00.000Z').join('\n')).toContain('Nothing new.')
  })
  test('datasheet rows render field names and option labels', () => {
    const fields = [{ id: 'fld_a', name: 'Company', type: 'text' }, { id: 'fld_b', name: 'Stage', type: 'single_select', options: [{ id: 'opt_1', label: 'Won' }] }]
    expect(rowLine({ id: 'r1', data: { fld_a: 'Acme', fld_b: 'opt_1', fld_x: null } }, fields)).toBe('r1  Company=Acme · Stage=Won')
  })
  test('flag specs', () => {
    expect(parseKr('Subscribers|5000|subs')).toEqual({ title: 'Subscribers', target_value: 5000, unit: 'subs' })
    expect(() => parseKr('X|lots')).toThrow(UsageError)
    expect(parseFieldSpec('Stage:single_select:New,Won:required')).toEqual({ name: 'Stage', type: 'single_select', options: ['New', 'Won'], required: true })
    expect(() => parseFieldSpec('Stage')).toThrow(UsageError)
  })
})

describe('argument parsing', () => {
  test('resolves groups, subcommands and global flags before the command', () => {
    expect(resolveCommand(['tasks', 'list', '--status', 'todo']).command?.path).toEqual(['tasks', 'list'])
    expect(resolveCommand(['--profile', 'x', 'tasks', 'get', 'id1']).command?.path).toEqual(['tasks', 'get'])
    expect(resolveCommand(['--json', 'context']).command?.path).toEqual(['context'])
    expect(resolveCommand(['profiles']).command?.path).toEqual(['profiles'])
    expect(resolveCommand(['profiles', 'use', 'x']).command?.path).toEqual(['profiles', 'use'])
    expect(resolveCommand(['tasks']).group).toBe('tasks')
    expect(() => resolveCommand(['tasks', 'frobnicate'])).toThrow(/Unknown command "gc tasks frobnicate"/)
    expect(() => resolveCommand(['frobnicate'])).toThrow(UsageError)
  })
  test('unknown flags, missing and extra arguments are usage errors', () => {
    const get = COMMANDS.find((c) => c.path.join(' ') === 'tasks get')!
    expect(() => parseCommandArgs(get, ['id', '--nope'])).toThrow(/Unknown option '--nope'/)
    expect(() => parseCommandArgs(get, [])).toThrow(/Missing argument/)
    expect(() => parseCommandArgs(get, ['a', 'b'])).toThrow(/Unexpected argument "b"/)
    expect(parseCommandArgs(get, ['id', '--json']).values.json).toBe(true)
  })

  const capture = () => {
    let out = ''
    let err = ''
    const sys: Sys = { env: { GC_CONFIG_DIR: dir }, stdout: (s) => { out += s }, stderr: (s) => { err += s }, stdinIsTTY: false, readStdin: async () => '', promptHidden: async () => '' }
    return { sys, get out() { return out }, get err() { return err } }
  }
  test('exit codes: help 0, usage 2, not connected 3', async () => {
    let c = capture()
    expect(await run(['--help'], c.sys)).toBe(0)
    expect(c.out).toContain('Usage: gc <command>')
    c = capture()
    expect(await run(['tasks', 'list', '--bogus'], c.sys)).toBe(2)
    expect(c.err).toContain("Unknown option '--bogus'")
    c = capture()
    expect(await run(['context'], c.sys)).toBe(3)
    expect(c.err).toContain('GROUNDCONTROL is not connected on this machine (no API key)')
    expect(c.err).toContain('registers at https://groundcontrol.makerslab.ai')
    expect(c.err).toContain('gc onboarding --token "gc_live_…"')
    c = capture()
    c.sys.env.GC_API_URL = 'http://localhost:3011/api/v1'
    expect(await run(['tasks', 'list'], c.sys)).toBe(3)
    expect(c.err).toContain('registers at http://localhost:3011 ')
    c = capture()
    expect(await run(['comment', 'id', 'a', 'b'], c.sys)).toBe(2)
    c = capture()
    expect(await run(['tasks', 'get', 'x', '--json', '--field', 'a'], c.sys)).toBe(2)
  })
  test('every command has a summary and at least one example', () => {
    for (const c of COMMANDS) {
      expect(c.summary.length).toBeGreaterThan(5)
      expect(c.examples?.length ?? 0).toBeGreaterThan(0)
    }
  })
})

describe('skills', () => {
  test('embedded copies equal the canonical .md files', () => {
    for (const s of SKILLS) {
      const file = readFileSync(join(import.meta.dir, '..', 'skills', s.name, 'SKILL.md'), 'utf8')
      expect(s.content).toBe(file)
    }
  })
  test('frontmatter has name + description, size is a thin bootstrap', () => {
    for (const s of SKILLS) {
      const fm = /^---\n([\s\S]*?)\n---\n/.exec(s.content)?.[1] ?? ''
      expect(fm).toContain(`name: ${s.name}`)
      expect(fm).toMatch(/^description: .{40,}$/m)
      expect(Buffer.byteLength(s.content)).toBeLessThan(3000)
    }
  })
  test('install writes per agent, skips modified files without --force', async () => {
    const home = join(dir, 'home')
    const { mkdirSync } = await import('node:fs')
    mkdirSync(join(home, '.claude'), { recursive: true })
    let out = ''
    const sys: Sys = { env: { HOME: home, GC_CONFIG_DIR: dir }, stdout: (s) => { out += s }, stderr: () => {}, stdinIsTTY: false, readStdin: async () => '', promptHidden: async () => '' }
    expect(await run(['skills', 'install'], sys)).toBe(0)
    const p = join(home, '.claude', 'skills', 'groundcontrol', 'SKILL.md')
    expect(readFileSync(p, 'utf8')).toBe(SKILLS[0].content)
    expect(existsSync(join(home, '.codex'))).toBe(false) // all = only agents that exist
    writeFileSync(p, 'edited')
    out = ''
    expect(await run(['skills', 'install', '--agent', 'claude'], sys)).toBe(0)
    expect(out).toContain('skipped')
    expect(readFileSync(p, 'utf8')).toBe('edited')
    expect(await run(['skills', 'install', '--agent', 'claude', '--force'], sys)).toBe(0)
    expect(readFileSync(p, 'utf8')).toBe(SKILLS[0].content)
  })
})
