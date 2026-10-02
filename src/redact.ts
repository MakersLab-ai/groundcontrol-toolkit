export function redactKey(input: string): string {
  return input.replace(/gc_live_[A-Za-z0-9_-]+/g, 'gc_live_<redacted>')
}
