#!/usr/bin/env bun
import { run } from './cli'
import type { Sys } from './command'
import { CliError } from './errors'

async function readStdin(): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of process.stdin) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

/** Reads a line from the TTY without echoing it (for the API key). */
function promptHidden(question: string): Promise<string> {
  const stdin = process.stdin
  process.stderr.write(question)
  return new Promise((resolve, reject) => {
    let buf = ''
    const cleanup = () => {
      stdin.off('data', onData)
      if (stdin.isTTY) stdin.setRawMode(false)
      stdin.pause()
      process.stderr.write('\n')
    }
    const onData = (data: Buffer | string) => {
      for (const ch of data.toString('utf8')) {
        if (ch === '\r' || ch === '\n' || ch === '\u0004') {
          cleanup()
          resolve(buf)
          return
        }
        if (ch === '\u0003') {
          cleanup()
          reject(new CliError('Aborted.', undefined, 130))
          return
        }
        if (ch === '\u007f' || ch === '\b') buf = buf.slice(0, -1)
        else if (ch >= ' ') buf += ch
      }
    }
    if (stdin.isTTY) stdin.setRawMode(true)
    stdin.resume()
    stdin.on('data', onData)
  })
}

const sys: Sys = {
  env: process.env,
  stdout: (s) => process.stdout.write(s),
  stderr: (s) => process.stderr.write(s),
  stdinIsTTY: Boolean(process.stdin.isTTY),
  readStdin,
  promptHidden,
  onSignal(handler) {
    const h = (sig: NodeJS.Signals) => handler(sig)
    process.on('SIGINT', h)
    process.on('SIGTERM', h)
    return () => {
      process.off('SIGINT', h)
      process.off('SIGTERM', h)
    }
  },
}

// Exit via the event loop draining (process.exitCode), not process.exit():
// exit() can cut off stdout when it is a pipe.
process.exitCode = await run(process.argv.slice(2), sys)
