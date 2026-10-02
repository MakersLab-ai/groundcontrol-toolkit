#!/usr/bin/env node
// Builds the single-file `gc` binaries with `bun build --compile`.
//
//   node scripts/build.mjs                                   all targets: compile + tar + checksums
//   node scripts/build.mjs --targets darwin-arm64,linux-amd64
//   node scripts/build.mjs --targets darwin-arm64 --no-package   compile only (sign, then …)
//   node scripts/build.mjs --targets darwin-arm64 --package-only tar an existing (signed) binary
//   node scripts/build.mjs --checksums-only                   checksums.txt over dist/*.tar.gz
//
// Layout: dist/gc_<os>_<arch>/gc, dist/gc_<os>_<arch>.tar.gz (one file `gc` inside),
// dist/checksums.txt ("<sha256>  <file>", the format `sha256sum -c` reads).
//
// darwin: an unsigned arm64 binary is killed by the kernel, so on a macOS host
// the binary is ad-hoc signed (`codesign --force --sign -`) before it is tarred.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const dist = join(root, 'dist')

// release name → bun --target
const TARGETS = {
  'darwin-arm64': 'bun-darwin-arm64',
  'darwin-amd64': 'bun-darwin-x64',
  'linux-amd64': 'bun-linux-x64',
  'linux-arm64': 'bun-linux-arm64',
}
const ALIASES = { 'darwin-x64': 'darwin-amd64', 'linux-x64': 'linux-amd64' }

const { values } = parseArgs({
  options: {
    targets: { type: 'string' },
    'no-package': { type: 'boolean' },
    'package-only': { type: 'boolean' },
    'checksums-only': { type: 'boolean' },
  },
})

function bunBin() {
  const local = join(root, 'node_modules', '.bin', 'bun')
  return existsSync(local) ? local : 'bun'
}

function run(cmd, args, opts = {}) {
  execFileSync(cmd, args, { stdio: 'inherit', cwd: root, ...opts })
}

function writeChecksums() {
  if (!existsSync(dist)) throw new Error('dist/ does not exist — nothing to checksum')
  const files = readdirSync(dist).filter((f) => /^gc_.*\.tar\.gz$/.test(f)).sort()
  if (!files.length) throw new Error('no dist/gc_*.tar.gz to checksum')
  const lines = files.map((f) => `${createHash('sha256').update(readFileSync(join(dist, f))).digest('hex')}  ${f}`)
  writeFileSync(join(dist, 'checksums.txt'), lines.join('\n') + '\n')
  console.log(`checksums.txt (${files.length} files)`)
}

if (values['checksums-only']) {
  writeChecksums()
  process.exit(0)
}

const requested = (values.targets ? values.targets.split(',') : Object.keys(TARGETS))
  .map((t) => t.trim())
  .filter(Boolean)
  .map((t) => ALIASES[t] ?? t)
for (const t of requested) {
  if (!TARGETS[t]) {
    console.error(`unknown target "${t}" — known: ${Object.keys(TARGETS).join(', ')}`)
    process.exit(2)
  }
}

mkdirSync(dist, { recursive: true })
for (const t of requested) {
  const name = `gc_${t.replace('-', '_')}`
  const outDir = join(dist, name)
  const bin = join(outDir, 'gc')

  if (!values['package-only']) {
    rmSync(outDir, { recursive: true, force: true })
    mkdirSync(outDir, { recursive: true })
    // No .env/bunfig autoload: a compiled Bun binary otherwise reads the .env of
    // whatever directory it runs in, so a project's GC_API_KEY would silently
    // override the saved profile (and every other secret in it would land in
    // gc's environment). Precedence is --token > exported GC_API_KEY > profile.
    run(bunBin(), ['build', 'src/main.ts', '--compile', '--no-compile-autoload-dotenv', '--no-compile-autoload-bunfig', `--target=${TARGETS[t]}`, '--minify', '--outfile', bin])
    if (t.startsWith('darwin') && process.platform === 'darwin') run('codesign', ['--force', '--sign', '-', bin])
  }
  if (values['no-package']) continue

  if (!existsSync(bin)) throw new Error(`missing ${bin} — build it first`)
  const tarball = join(dist, `${name}.tar.gz`)
  rmSync(tarball, { force: true })
  run('tar', ['-czf', tarball, '-C', outDir, 'gc'])
  console.log(`${name}.tar.gz  ${(statSync(tarball).size / 1024 / 1024).toFixed(1)} MB  (binary ${(statSync(bin).size / 1024 / 1024).toFixed(1)} MB)`)
}

if (!values['no-package']) writeChecksums()
