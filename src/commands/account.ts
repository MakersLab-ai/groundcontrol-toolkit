import { GroundControlClient } from '../client'
import { str, type CommandDef } from '../command'
import { DEFAULT_API_URL, configPath, loadConfig, redactKey, saveConfig, slugify } from '../config'
import { CliError, UsageError, isUnauthorized } from '../errors'

export const accountCommands: CommandDef[] = [
  {
    path: ['onboarding'],
    summary: 'Connect gc to a workspace with an agent API key and save it as a profile',
    options: {
      'skip-verify': { type: 'boolean', desc: 'Save without calling /me (offline setup)' },
    },
    details: [
      'The key comes from --token <key>, --token - (stdin), an interactive hidden prompt (TTY),',
      'or stdin when nothing is attached. It is verified with /me and stored in config.json',
      '(dir 0700, file 0600). The profile is named after the workspace unless --profile is given,',
      'and becomes the current one. The key is never printed.',
    ].join('\n'),
    examples: [
      'gc onboarding --token gc_live_…',
      'printf %s "$KEY" | gc onboarding --token -',
      'gc onboarding --profile acme --token gc_live_…',
    ],
    async run(ctx) {
      const { values, sys, out } = ctx
      let token = typeof values.token === 'string' ? values.token : undefined
      if (token === '-' || (token === undefined && !sys.stdinIsTTY)) token = await sys.readStdin()
      else if (token === undefined) token = await sys.promptHidden('GROUNDCONTROL API key (gc_live_…): ')
      token = token.trim()
      if (!token) throw new UsageError('No API key given.', 'Run: gc onboarding --token gc_live_…   (create one in GROUNDCONTROL → Settings → API Keys)')
      if (/\s/.test(token)) throw new UsageError('The API key contains whitespace — paste only the key.')

      const cfg = loadConfig(sys.env)
      const requested = str(values, 'profile')
      const existing = requested ? cfg.profiles[requested] : undefined
      const apiUrl = (str(values, 'api-url') || sys.env.GC_API_URL || existing?.api_url || DEFAULT_API_URL).replace(/\/+$/, '')

      let workspace = existing?.workspace ?? ''
      let agent = existing?.agent ?? ''
      let workflow: string | undefined
      if (values['skip-verify'] !== true) {
        const client = new GroundControlClient({ apiUrl, apiKey: token })
        let me: any
        try {
          me = await client.getMe()
        } catch (e) {
          if (isUnauthorized(e)) {
            throw new CliError(`The API key was rejected by ${apiUrl} (401). Nothing was saved.`, 'Check the key (GROUNDCONTROL → Settings → API Keys) — it may be revoked or from another server (--api-url).', 3)
          }
          throw new CliError(`Key check failed against ${apiUrl}: ${(e as Error).message}`, 'Check the key (Settings → API Keys) and --api-url. Nothing was saved.')
        }
        workspace = me?.data?.tenant?.name ?? ''
        agent = me?.data?.user?.display_name ?? ''
        workflow = me?.data?.tenant?.workflow
        if (me?.data?.user?.is_agent === false) out.err('warning: this key belongs to a human member, not an agent.')
      } else if (!requested) {
        throw new UsageError('--skip-verify needs --profile <name> (the workspace name is unknown without /me).')
      }

      const profileName = requested || slugify(workspace)
      cfg.profiles[profileName] = { api_url: apiUrl, api_key: token, workspace, agent, created_at: new Date().toISOString() }
      cfg.current = profileName
      const path = saveConfig(cfg, sys.env)

      const result = { profile: profileName, workspace, agent, workflow: workflow ?? null, api_url: apiUrl, key: redactKey(token), config: path }
      out.emit(result, () => [
        values['skip-verify'] === true ? 'Saved (not verified).' : `Connected to workspace "${workspace}" as "${agent}"${workflow ? ` (${workflow})` : ''}.`,
        `Profile: ${profileName} (current) · key ${redactKey(token)} · ${apiUrl}`,
        `Config:  ${path}`,
        sys.env.GC_API_KEY ? 'note: GC_API_KEY is set in this shell and overrides the saved profile.' : '',
        'Next:    gc context',
      ].filter(Boolean))
    },
  },
  {
    path: ['profiles'],
    summary: 'List saved profiles (workspaces); * marks the current one',
    examples: ['gc profiles', 'gc profiles use acme', 'gc profiles remove old-workspace'],
    async run(ctx) {
      const cfg = loadConfig(ctx.sys.env)
      const active = ctx.sys.env.GC_PROFILE || cfg.current
      const rows = Object.entries(cfg.profiles).map(([n, p]) => ({
        name: n, current: n === cfg.current, workspace: p.workspace, agent: p.agent, api_url: p.api_url, key: redactKey(p.api_key), created_at: p.created_at,
      }))
      ctx.out.emit({ current: cfg.current, config: configPath(ctx.sys.env), profiles: rows }, () => {
        if (rows.length === 0) return `No profiles. Run: gc onboarding --token gc_live_…`
        const lines = rows.map((r) => `${r.name === active ? '*' : ' '} ${r.name}  ${r.workspace || '-'}  as ${r.agent || '-'}  ${r.key}  ${r.api_url}`)
        if (ctx.sys.env.GC_PROFILE) lines.push(`(GC_PROFILE=${ctx.sys.env.GC_PROFILE} selects the profile in this shell)`)
        if (ctx.sys.env.GC_API_KEY) lines.push('(GC_API_KEY is set and overrides every profile key in this shell)')
        return lines
      })
    },
  },
  {
    path: ['profiles', 'use'],
    summary: 'Make a profile the current one',
    args: '<name>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc profiles use acme'],
    async run(ctx) {
      const cfg = loadConfig(ctx.sys.env)
      const n = ctx.args[0]
      if (!cfg.profiles[n]) throw new CliError(`Unknown profile "${n}". Known: ${Object.keys(cfg.profiles).join(', ') || '(none)'}.`)
      cfg.current = n
      saveConfig(cfg, ctx.sys.env)
      ctx.out.emit({ current: n }, () => `Current profile: ${n} (${cfg.profiles[n].workspace})`)
    },
  },
  {
    path: ['profiles', 'remove'],
    summary: 'Delete a saved profile (the key stays valid on the server — revoke it there)',
    args: '<name>',
    minArgs: 1,
    maxArgs: 1,
    examples: ['gc profiles remove old-workspace'],
    async run(ctx) {
      const cfg = loadConfig(ctx.sys.env)
      const n = ctx.args[0]
      if (!cfg.profiles[n]) throw new CliError(`Unknown profile "${n}".`)
      delete cfg.profiles[n]
      if (cfg.current === n) cfg.current = Object.keys(cfg.profiles)[0] ?? null
      saveConfig(cfg, ctx.sys.env)
      ctx.out.emit({ removed: n, current: cfg.current }, () => `Removed ${n}. Current: ${cfg.current ?? '(none)'}`)
    },
  },
]
