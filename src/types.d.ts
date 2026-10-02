// Bun text imports (`import x from './f.md' with { type: 'text' }`) — the
// skills are embedded into the compiled binary this way.
declare module '*.md' {
  const content: string
  export default content
}
