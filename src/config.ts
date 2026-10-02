import { chmodSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync, unlinkSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { CliError, NotConnectedError } from './errors'

export const DEFAULT_API_URL = 'https://groundcontrol.makerslab.ai/api/v1'

export interface Profile {
  api_url: string
  api_key: string
  workspace: string
  agent: string
  created_at: string
}

export interface ConfigFile {
  current: string | null
  profiles: Record<string, Profile>
}

type Env = Record<string, string | undefined>

/** `$GC_CONFIG_DIR` > `$XDG_CONFIG_HOME/groundcontrol` > `~/.config/groundcontrol`. */
export function configDir(env: Env = process.env): string {
  if (env.GC_CONFIG_DIR) return env.GC_CONFIG_DIR
  if (env.XDG_CONFIG_HOME) return join(env.XDG_CONFIG_HOME, 'groundcontrol')
  return join(env.HOME || homedir(), '.config', 'groundcontrol')
}

export function configPath(env: Env = process.env): string {
  return join(configDir(env), 'config.json')
}

export function loadConfig(env: Env = process.env): ConfigFile {
  const path = configPath(env)
  if (!existsSync(path)) return { current: null, profiles: {} }
  let parsed: unknown
  try {
    parsed = JSON.parse(readFileSync(path, 'utf8'))
  } catch {
    throw new CliError(`Config file is not valid JSON: ${path}. Fix or delete it, then run gc onboarding again.`)
  }
  const cfg = (parsed ?? {}) as Partial<ConfigFile>
  return {
    current: typeof cfg.current === 'string' ? cfg.current : null,
    profiles: cfg.profiles && typeof cfg.profiles === 'object' ? cfg.profiles : {},
  }
}

/**
 * Writes the config the only way a file holding API keys should be written:
 * directory 0700, file 0600 from the first byte (the tmp file is created with
 * that mode, never chmod-ed after the fact), and an atomic rename so a crash or
 * a concurrent reader never sees half a file.
 */
export function saveConfig(cfg: ConfigFile, env: Env = process.env): string {
  const dir = configDir(env)
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  chmodSync(dir, 0o700)
  const path = configPath(env)
  atomicWrite(path, JSON.stringify(cfg, null, 2) + '\n', 0o600)
  return path
}

export function atomicWrite(path: string, content: string, mode = 0o600): void {
  const tmp = `${path}.${process.pid}.${Date.now()}.tmp`
  try {
    writeFileSync(tmp, content, { mode, flag: 'wx' })
    chmodSync(tmp, mode) // umask can strip bits from `mode`; make it exact
    renameSync(tmp, path)
  } catch (e) {
    try { unlinkSync(tmp) } catch { /* already gone */ }
    throw e
  }
}

/** `gc_live_…abcd` — never more than the last four characters. */
export function redactKey(key: string | undefined | null): string {
  if (!key) return '(none)'
  const prefix = key.startsWith('gc_live_') ? 'gc_live_' : ''
  return `${prefix}…${key.length > 12 ? key.slice(-4) : ''}`
}

export function slugify(name: string): string {
  const slug = name
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
  return slug || 'default'
}

export interface GlobalFlags {
  token?: string
  profile?: string
  'api-url'?: string
}

export interface ResolvedAuth {
  apiUrl: string
  apiKey: string | undefined
  /** The profile that supplied the key/url, if one was selected. */
  profileName: string | undefined
  keySource: 'flag' | 'env' | 'profile' | 'none'
}

/**
 * Key: `--token` > `GC_API_KEY` > profile. URL: `--api-url` > `GC_API_URL` >
 * profile > default. Profile: `--profile` > `GC_PROFILE` > `current`.
 * A profile named explicitly that doesn't exist is an error — silently falling
 * back to another workspace would write into the wrong board.
 */
export function resolveAuth(flags: GlobalFlags, env: Env = process.env, cfg: ConfigFile = loadConfig(env)): ResolvedAuth {
  const explicit = flags.profile || env.GC_PROFILE
  const profileName = explicit || cfg.current || undefined
  const profile = profileName ? cfg.profiles[profileName] : undefined
  if (explicit && !profile) {
    const known = Object.keys(cfg.profiles)
    throw new CliError(
      `Unknown profile "${explicit}".` + (known.length ? ` Known: ${known.join(', ')}.` : ' No profiles yet — run gc onboarding.'),
    )
  }

  let apiKey: string | undefined
  let keySource: ResolvedAuth['keySource'] = 'none'
  if (flags.token) { apiKey = flags.token; keySource = 'flag' }
  else if (env.GC_API_KEY) { apiKey = env.GC_API_KEY; keySource = 'env' }
  else if (profile?.api_key) { apiKey = profile.api_key; keySource = 'profile' }

  const apiUrl = (flags['api-url'] || env.GC_API_URL || profile?.api_url || DEFAULT_API_URL).replace(/\/+$/, '')
  return { apiUrl, apiKey, profileName: profile ? profileName : undefined, keySource }
}

export function requireKey(auth: ResolvedAuth): string {
  if (!auth.apiKey) throw new NotConnectedError('no API key', auth.apiUrl)
  return auth.apiKey
}
