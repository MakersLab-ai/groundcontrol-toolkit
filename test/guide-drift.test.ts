// The know-how agents read (the server guide behind `gc guide`, the bootstrap
// skills) names `gc` commands and flags. They ship on different schedules — the
// guide with a GROUNDCONTROL deploy, the binary with a release of this repo —
// so a renamed flag would leave agents following instructions that exit 2.
// This pins every `gc …` snippet in those texts to the command table.
//
// The guide is read LIVE from the server (public, no key): GC_GUIDE_URL, default
// production. CI runs this on every push and daily, so a guide deploy that names
// a command this CLI doesn't have is caught within a day. GC_GUIDE_URL=off skips
// the live part (offline work); a server without the guide yet (404) is skipped
// with a note, not failed.
import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { COMMANDS } from '../src/cli'
import { GLOBAL_OPTIONS } from '../src/command'

const root = join(import.meta.dir, '..')
const GUIDE_URL = process.env.GC_GUIDE_URL ?? 'https://groundcontrol.makerslab.ai/api/v1/guide'

async function guideTexts(): Promise<[string, string][]> {
  if (GUIDE_URL === 'off') return []
  const list = await fetch(GUIDE_URL)
  if (list.status === 404) {
    console.warn(`guide-drift: ${GUIDE_URL} answers 404 (guide not deployed there yet) — skipping the live guide`)
    return []
  }
  if (!list.ok) throw new Error(`guide-drift: ${GUIDE_URL} answered ${list.status}`)
  const topics: { topic: string }[] = (await list.json()).data
  return Promise.all(topics.map(async ({ topic }) => {
    const res = await fetch(`${GUIDE_URL}/${encodeURIComponent(topic)}`)
    if (!res.ok) throw new Error(`guide-drift: ${GUIDE_URL}/${topic} answered ${res.status}`)
    return [`guide:${topic}`, (await res.json()).data.body] as [string, string]
  }))
}

const texts: [string, string][] = [
  ...(await guideTexts()),
  ['skill:groundcontrol', readFileSync(join(root, 'skills/groundcontrol/SKILL.md'), 'utf8')],
  ['skill:groundcontrol-datasheets', readFileSync(join(root, 'skills/groundcontrol-datasheets/SKILL.md'), 'utf8')],
]

/** Every `gc …` invocation: inline code spans and code-block lines. */
function snippets(text: string): string[] {
  const out: string[] = []
  for (const m of text.matchAll(/`(gc [^`\n]+)`/g)) out.push(m[1])
  for (const block of text.matchAll(/```[a-z]*\n([\s\S]*?)```/g)) {
    for (const line of block[1].split('\n')) if (/^\s*gc /.test(line)) out.push(line.trim())
  }
  // One span may chain several calls: `gc a · gc b`, `gc a && gc b`, `x | gc b`.
  return out
    .flatMap((s) => s.replace(/\\/g, '').split(/\s+(?:·|&&|\|\||\||;)\s+/))
    .map((s) => s.trim())
    .filter((s) => s.startsWith('gc '))
}

function resolve(words: string[]) {
  // Longest command path that prefixes the words.
  return COMMANDS
    .filter((c) => c.path.every((p, i) => words[i] === p))
    .sort((a, b) => b.path.length - a.path.length)[0]
}

describe('gc commands named in agent-facing texts exist', () => {
  for (const [name, text] of texts) {
    test(name, () => {
      const found = snippets(text)
      for (const s of found) {
        const words = s.split(/\s+/).slice(1)
        // `gc tasks …` / `gc <cmd> --help` style placeholders are fine.
        if (words[0].startsWith('<') || words[0] === '…' || words[0] === '...') continue
        // A group mention (`gc tables …`) is fine while commands live under it.
        if (!words[1] || /^(…|\.\.\.|<)/.test(words[1]) || words[1].startsWith('--')) {
          if (!resolve(words) && COMMANDS.some((c) => c.path[0] === words[0])) continue
        }
        const cmd = resolve(words)
        expect(cmd, `"${s}" (${name}) names no gc command`).toBeDefined()
        for (const flag of s.matchAll(/--([a-z][a-z-]*)/g)) {
          const known = flag[1] === 'help' || flag[1] in (cmd!.options ?? {}) || flag[1] in GLOBAL_OPTIONS
          expect(known, `"${s}" (${name}): --${flag[1]} is not a flag of gc ${cmd!.path.join(' ')}`).toBe(true)
        }
      }
    })
  }
})
