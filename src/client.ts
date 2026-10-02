import { readFile } from 'node:fs/promises'
import { basename, extname } from 'node:path'
import { redactKey } from './redact.js'

// Content types for the file kinds that actually get attached to tasks. The API
// falls back to application/octet-stream when the type is empty, which makes a
// browser download the file as a blob instead of previewing it — so infer from
// the extension rather than sending nothing.
const MIME_BY_EXT: Record<string, string> = {
  '.pdf': 'application/pdf',
  '.doc': 'application/msword',
  '.docx': 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  '.xls': 'application/vnd.ms-excel',
  '.xlsx': 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  '.ppt': 'application/vnd.ms-powerpoint',
  '.pptx': 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  '.txt': 'text/plain',
  '.md': 'text/markdown',
  '.csv': 'text/csv',
  '.json': 'application/json',
  '.yaml': 'application/yaml',
  '.yml': 'application/yaml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.svg': 'image/svg+xml',
  '.webp': 'image/webp',
  '.zip': 'application/zip',
}

export function contentTypeFor(fileName: string): string {
  return MIME_BY_EXT[extname(fileName).toLowerCase()] ?? 'application/octet-stream'
}

export interface ClientConfig {
  apiUrl: string
  apiKey: string
  sessionId?: string
}

export class GroundControlClient {
  private baseUrl: string
  private apiKey: string
  private sessionId?: string

  constructor(config: ClientConfig) {
    this.baseUrl = config.apiUrl.replace(/\/$/, '')
    this.apiKey = config.apiKey
    this.sessionId = config.sessionId
  }

  private async request(method: string, path: string, body?: unknown): Promise<any> {
    const url = `${this.baseUrl}${path}`
    const res = await fetch(url, {
      method,
      headers: {
        Authorization: `Bearer ${this.apiKey}`,
        'Content-Type': 'application/json',
        ...(this.sessionId ? { 'X-GC-Session-Id': this.sessionId } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
    })

    return this.handle(res)
  }

  // Multipart sibling of request(). Deliberately does NOT set Content-Type:
  // fetch has to generate the multipart boundary itself, and pinning the header
  // would produce a body the server can't parse.
  private async requestForm(method: string, path: string, form: FormData): Promise<any> {
    const res = await fetch(`${this.baseUrl}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${this.apiKey}`,
        ...(this.sessionId ? { 'X-GC-Session-Id': this.sessionId } : {}),
      },
      body: form,
    })

    return this.handle(res)
  }

  private async handle(res: Response): Promise<any> {
    if (!res.ok) {
      const errBody = await res.json().catch(() => ({ error: { code: 'unknown', message: res.statusText } }))
      const message = errBody.error?.message || res.statusText
      throw new Error(`GROUNDCONTROL API error ${res.status}: ${redactKey(message)}`)
    }

    if (res.status === 204) return null
    return res.json()
  }

  private qs(params?: Record<string, string | number | undefined>): string {
    if (!params) return ''
    const entries = Object.entries(params).filter(([, v]) => v !== undefined) as [string, string | number][]
    if (entries.length === 0) return ''
    return '?' + new URLSearchParams(entries.map(([k, v]) => [k, String(v)])).toString()
  }

  // Auth / context
  getMe() { return this.request('GET', '/me') }
  getChanges(since: string) { return this.request('GET', `/changes${this.qs({ since })}`) }
  search(params: Record<string, string | number | undefined>) { return this.request('GET', `/search${this.qs(params)}`) }
  semanticSearch(input: { query: string; types?: string[]; limit?: number }) {
    return this.request('POST', '/search/semantic', input)
  }

  // Tasks
  listTasks(params?: Record<string, string | number | undefined>) { return this.request('GET', `/tasks${this.qs(params)}`) }
  getTask(id: string) { return this.request('GET', `/tasks/${id}`) }
  createTask(input: unknown) { return this.request('POST', '/tasks', input) }
  updateTask(id: string, input: unknown) { return this.request('PATCH', `/tasks/${id}`, input) }
  // The route pages 50 comments per call (max 100), oldest first — a single
  // call on a long thread returned only the OLDEST ones and silently dropped
  // the newest, i.e. the ones an agent most needs (task 425a1ed4). Page to the
  // end; `total` is re-read per page so a comment added mid-walk is included.
  async listComments(taskId: string) {
    const data: any[] = []
    let total = Infinity
    while (data.length < total) {
      const page = await this.request('GET', `/tasks/${taskId}/comments?limit=100&offset=${data.length}`)
      const rows = page?.data ?? []
      total = page?.meta?.total ?? 0
      if (rows.length === 0) break
      data.push(...rows)
    }
    return { data, meta: { total: data.length } }
  }
  createComment(taskId: string, body: string) { return this.request('POST', `/tasks/${taskId}/comments`, { body }) }
  listAttachments(taskId: string) { return this.request('GET', `/tasks/${taskId}/attachments`) }
  // Reads a local file and posts it as multipart/form-data. The API caps
  // uploads at 50 MB and accepts any file type.
  async uploadAttachment(taskId: string, filePath: string, fileName?: string) {
    const bytes = await readFile(filePath)
    const name = fileName || basename(filePath)
    const form = new FormData()
    form.append('file', new Blob([bytes], { type: contentTypeFor(name) }), name)
    return this.requestForm('POST', `/tasks/${taskId}/attachments`, form)
  }

  // Initiatives
  listInitiatives() { return this.request('GET', '/initiatives') }
  getInitiative(id: string) { return this.request('GET', `/initiatives/${id}`) }
  createInitiative(input: unknown) { return this.request('POST', '/initiatives', input) }
  updateInitiative(id: string, input: unknown) { return this.request('PATCH', `/initiatives/${id}`, input) }
  getInitiativeMemory(id: string) { return this.request('GET', `/initiatives/${id}/memory`) }

  // Goals (API: objectives)
  listObjectives(params?: Record<string, string | number | undefined>) { return this.request('GET', `/objectives${this.qs(params)}`) }
  getObjective(id: string) { return this.request('GET', `/objectives/${id}`) }
  createObjective(input: unknown) { return this.request('POST', '/objectives', input) }
  updateObjective(id: string, input: unknown) { return this.request('PATCH', `/objectives/${id}`, input) }
  submitGoalCheckin(objectiveId: string, checkinId: string, body: unknown) {
    return this.request('POST', `/objectives/${objectiveId}/checkins/${checkinId}/submit`, body)
  }
  updateKeyResult(objectiveId: string, krId: string, input: Record<string, unknown>) {
    return this.request('PATCH', `/objectives/${objectiveId}/key-results`, { kr_id: krId, ...input })
  }

  // Docs
  listDocs(params?: Record<string, string | number | undefined>) { return this.request('GET', `/docs${this.qs(params)}`) }
  getDoc(id: string) { return this.request('GET', `/docs/${id}`) }
  createDoc(input: unknown) { return this.request('POST', '/docs', input) }
  updateDoc(id: string, input: unknown) { return this.request('PATCH', `/docs/${id}`, input) }
  archiveDoc(id: string) { return this.request('DELETE', `/docs/${id}`) }
  createDocComment(docId: string, body: string) { return this.request('POST', `/docs/${docId}/comments`, { body }) }

  // Journal
  listJournal(params?: Record<string, string | number | undefined>) { return this.request('GET', `/journal${this.qs(params)}`) }
  getJournalDay(date: string) { return this.request('GET', `/journal/${date}`) }
  saveJournalSummary(date: string, agentSummary: string) {
    return this.request('POST', `/journal/${date}/summary`, { agent_summary: agentSummary })
  }

  // Datasheets (called "tables" on the wire: /api/v1/tables…). Every one of
  // these 404s in a workspace without the feature flag — the same answer as a
  // datasheet that does not exist, so the module's existence doesn't leak.
  // Ported from openclaw-plugin/src/client.ts (canonical); keep them in sync.
  listTables(params?: Record<string, string | number | undefined>) { return this.request('GET', `/tables${this.qs(params)}`) }
  getTable(id: string) { return this.request('GET', `/tables/${id}`) }
  createTable(input: unknown) { return this.request('POST', '/tables', input) }
  updateTable(id: string, input: unknown) { return this.request('PATCH', `/tables/${id}`, input) }
  deleteTable(id: string) { return this.request('DELETE', `/tables/${id}`) }
  addField(tableId: string, input: unknown) { return this.request('POST', `/tables/${tableId}/fields`, input) }
  updateField(tableId: string, fieldId: string, input: unknown) { return this.request('PATCH', `/tables/${tableId}/fields/${fieldId}`, input) }
  deleteField(tableId: string, fieldId: string) { return this.request('DELETE', `/tables/${tableId}/fields/${fieldId}`) }

  // One page of rows, exactly as asked for.
  listRows(tableId: string, params?: Record<string, string | number | undefined>) {
    return this.request('GET', `/tables/${tableId}/rows${this.qs(params)}`)
  }
  // Every row matching the query, paged like listComments(): the route caps
  // `limit` at 200, so "give me the datasheet" is several requests and a single
  // call would silently answer with the first 200 rows — a truncated list and a
  // complete one look identical to the reader. The order is deterministic
  // (chosen sort, then `id`), which is what makes offset paging safe; rows are
  // still deduped by id because a row inserted mid-walk shifts the boundary and
  // hands the same row to two pages. `maxPages` is a fan-out backstop, not a
  // display limit — when it bites, `meta.truncated` says so instead of the
  // answer just ending.
  async listAllRows(tableId: string, params?: Record<string, string | number | undefined>, maxPages = 25) {
    const PAGE = 200
    const seen = new Set<string>()
    const data: any[] = []
    // The offset cursor counts the rows the SERVER handed over, not the ones
    // that survived the dedupe — deriving the offset from `data.length` re-asks
    // for the same page forever as soon as one duplicate is dropped.
    let fetched = 0
    let matched = 0
    for (let page = 0; ; page++) {
      if (page >= maxPages) return { data, meta: { total: data.length, matched, truncated: true } }
      const res = await this.listRows(tableId, { ...(params ?? {}), limit: PAGE, offset: fetched })
      const rows: any[] = res?.data ?? []
      matched = res?.meta?.total ?? 0
      fetched += rows.length
      for (const r of rows) {
        if (r?.id) {
          if (seen.has(r.id)) continue
          seen.add(r.id)
        }
        data.push(r)
      }
      if (rows.length < PAGE || fetched >= matched) break
    }
    return { data, meta: { total: data.length, matched, truncated: false } }
  }

  createRows(tableId: string, rows: unknown[]) { return this.request('POST', `/tables/${tableId}/rows`, { rows: rows.map((data) => ({ data })) }) }
  updateRows(tableId: string, rows: { id: string; data: unknown }[]) { return this.request('PATCH', `/tables/${tableId}/rows`, { rows }) }
  deleteRows(tableId: string, ids: string[]) { return this.request('DELETE', `/tables/${tableId}/rows`, { ids }) }
  createTableComment(tableId: string, body: string) { return this.request('POST', `/tables/${tableId}/comments`, { body }) }
}
