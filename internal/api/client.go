// Package api is the HTTP client for the GROUNDCONTROL REST API
// (https://groundcontrol.makerslab.ai/api/v1). The contract is the server's
// public/openapi.yaml and the agent guide — there is no shared client code.
package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

// Error is a non-2xx answer: `GROUNDCONTROL API error <status>: <message>`.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("GROUNDCONTROL API error %d: %s", e.Status, e.Message)
}

var keyRe = regexp.MustCompile(`gc_live_[A-Za-z0-9_-]+`)

// RedactKeys removes API keys from text that came back from the server.
func RedactKeys(s string) string { return keyRe.ReplaceAllString(s, "gc_live_<redacted>") }

// Content types for the file kinds that actually get attached to tasks. The API
// falls back to application/octet-stream when the type is empty, which makes a
// browser download the file as a blob instead of previewing it — so infer from
// the extension rather than sending nothing.
var mimeByExt = map[string]string{
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".txt":  "text/plain",
	".md":   "text/markdown",
	".csv":  "text/csv",
	".json": "application/json",
	".yaml": "application/yaml",
	".yml":  "application/yaml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
	".zip":  "application/zip",
}

func ContentTypeFor(fileName string) string {
	if t, ok := mimeByExt[strings.ToLower(filepath.Ext(fileName))]; ok {
		return t
	}
	return "application/octet-stream"
}

// Timeouts: a JSON call that takes longer than RequestTimeout is a stalled
// server, not a slow answer. Only an upload (up to 50 MB) gets UploadTimeout.
const (
	RequestTimeout = 30 * time.Second
	UploadTimeout  = 5 * time.Minute
)

type Client struct {
	BaseURL   string
	APIKey    string // empty: no Authorization header (public endpoints)
	SessionID string
	HTTP      *http.Client // JSON calls
	Upload    *http.Client // multipart uploads
	// Ctx cancels in-flight requests (gc listen cancels it on SIGINT/SIGTERM).
	Ctx context.Context
}

func New(apiURL, apiKey, sessionID string) *Client {
	return &Client{
		BaseURL:   strings.TrimSuffix(apiURL, "/"),
		APIKey:    apiKey,
		SessionID: sessionID,
		HTTP:      &http.Client{Timeout: RequestTimeout},
		Upload:    &http.Client{Timeout: UploadTimeout},
		Ctx:       context.Background(),
	}
}

// seg escapes an id for use as one path segment: `../x` or `a?b` can't change
// the path or the query.
func seg(id string) string {
	if id == "." || id == ".." {
		return strings.ReplaceAll(id, ".", "%2E")
	}
	return url.PathEscape(id)
}

// Params is an ordered query string; Undefined values are dropped.
type Params = *js.Object

func qs(p Params) string {
	if p == nil || p.Len() == 0 {
		return ""
	}
	parts := []string{}
	for _, k := range p.Keys() {
		v, _ := p.Get(k)
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(js.Str(v)))
	}
	return "?" + strings.Join(parts, "&")
}

func (c *Client) header(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.SessionID != "" {
		req.Header.Set("X-GC-Session-Id", c.SessionID)
	}
}

// Request sends JSON (body may be nil) and decodes the JSON answer.
func (c *Client) Request(method, path string, body any) (any, error) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(js.Stringify(body))
	}
	req, err := http.NewRequestWithContext(c.Ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	c.header(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(c.HTTP, req)
}

func (c *Client) do(hc *http.Client, req *http.Request) (any, error) {
	res, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("Cannot reach %s: %s", c.BaseURL, RedactKeys(err.Error()))
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("Reading the response from %s failed: %w", c.BaseURL, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := http.StatusText(res.StatusCode)
		if parsed, err := js.Parse(data); err == nil {
			if m := js.Get(parsed, "error", "message"); js.Truthy(m) {
				msg = js.Str(m)
			}
		}
		return nil, &Error{Status: res.StatusCode, Message: RedactKeys(msg)}
	}
	if res.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	v, err := js.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("The response from %s is not valid JSON: %s", c.BaseURL, err)
	}
	return v, nil
}

// ─── endpoints ────────────────────────────────────────────────────────────────

func (c *Client) Get(path string) (any, error) { return c.Request("GET", path, nil) }

func (c *Client) GetMe() (any, error) { return c.Get("/me") }

func (c *Client) GetChanges(since string) (any, error) {
	return c.Get("/changes" + qs(js.O("since", since)))
}

func (c *Client) Search(p Params) (any, error) { return c.Get("/search" + qs(p)) }

func (c *Client) SemanticSearch(body *js.Object) (any, error) {
	return c.Request("POST", "/search/semantic", body)
}

// Tasks

func (c *Client) ListTasks(p Params) (any, error) { return c.Get("/tasks" + qs(p)) }
func (c *Client) GetTask(id string) (any, error)  { return c.Get("/tasks/" + seg(id)) }
func (c *Client) CreateTask(body any) (any, error) {
	return c.Request("POST", "/tasks", body)
}
func (c *Client) UpdateTask(id string, body any) (any, error) {
	return c.Request("PATCH", "/tasks/"+seg(id), body)
}

// ListComments pages to the end. The route pages 50 comments per call (max
// 100), oldest first — a single call on a long thread returned only the OLDEST
// ones and silently dropped the newest, i.e. the ones an agent most needs.
// `total` is re-read per page so a comment added mid-walk is included.
func (c *Client) ListComments(taskID string) (any, error) {
	data := []any{}
	total := -1.0
	for total < 0 || float64(len(data)) < total {
		page, err := c.Get("/tasks/" + seg(taskID) + "/comments?limit=100&offset=" + strconv.Itoa(len(data)))
		if err != nil {
			return nil, err
		}
		rows := js.List(js.Get(page, "data"))
		total = js.Number(js.Or(js.Get(page, "meta", "total"), 0.0))
		if len(rows) == 0 {
			break
		}
		data = append(data, rows...)
	}
	return js.O("data", data, "meta", js.O("total", len(data))), nil
}

func (c *Client) CreateComment(taskID, body string) (any, error) {
	return c.Request("POST", "/tasks/"+seg(taskID)+"/comments", js.O("body", body))
}

func (c *Client) ListAttachments(taskID string) (any, error) {
	return c.Get("/tasks/" + seg(taskID) + "/attachments")
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "%0D", "\n", "%0A")

// UploadAttachment posts a local file as multipart/form-data (the API caps
// uploads at 50 MB). The Content-Type header is exactly what mime/multipart
// produces, boundary included — never pinned by hand.
func (c *Client) UploadAttachment(taskID, filePath, fileName string) (any, error) {
	bytesIn, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	name := fileName
	if name == "" {
		name = filepath.Base(filePath)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, quoteEscaper.Replace(name)))
	h.Set("Content-Type", ContentTypeFor(name))
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(bytesIn); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(c.Ctx, "POST", c.BaseURL+"/tasks/"+seg(taskID)+"/attachments", &buf)
	if err != nil {
		return nil, err
	}
	c.header(req)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.do(c.Upload, req)
}

// Initiatives

func (c *Client) GetInitiative(id string) (any, error) { return c.Get("/initiatives/" + seg(id)) }
func (c *Client) GetInitiativeMemory(id string) (any, error) {
	return c.Get("/initiatives/" + seg(id) + "/memory")
}

// Goals (API: objectives)

func (c *Client) ListObjectives(p Params) (any, error) { return c.Get("/objectives" + qs(p)) }
func (c *Client) GetObjective(id string) (any, error)  { return c.Get("/objectives/" + seg(id)) }
func (c *Client) CreateObjective(body any) (any, error) {
	return c.Request("POST", "/objectives", body)
}
func (c *Client) UpdateObjective(id string, body any) (any, error) {
	return c.Request("PATCH", "/objectives/"+seg(id), body)
}
func (c *Client) SubmitGoalCheckin(objectiveID, checkinID string, body any) (any, error) {
	return c.Request("POST", "/objectives/"+seg(objectiveID)+"/checkins/"+seg(checkinID)+"/submit", body)
}
func (c *Client) UpdateKeyResult(objectiveID, krID string, input *js.Object) (any, error) {
	body := js.O("kr_id", krID)
	for _, k := range input.Keys() {
		v, _ := input.Get(k)
		body.Set(k, v)
	}
	return c.Request("PATCH", "/objectives/"+seg(objectiveID)+"/key-results", body)
}

// Docs

func (c *Client) ListDocs(p Params) (any, error)  { return c.Get("/docs" + qs(p)) }
func (c *Client) GetDoc(id string) (any, error)   { return c.Get("/docs/" + seg(id)) }
func (c *Client) CreateDoc(body any) (any, error) { return c.Request("POST", "/docs", body) }
func (c *Client) UpdateDoc(id string, body any) (any, error) {
	return c.Request("PATCH", "/docs/"+seg(id), body)
}
func (c *Client) ArchiveDoc(id string) (any, error) {
	return c.Request("DELETE", "/docs/"+seg(id), nil)
}
func (c *Client) CreateDocComment(docID, body string) (any, error) {
	return c.Request("POST", "/docs/"+seg(docID)+"/comments", js.O("body", body))
}

// Journal

func (c *Client) ListJournal(p Params) (any, error) { return c.Get("/journal" + qs(p)) }
func (c *Client) GetJournalDay(date string) (any, error) {
	return c.Get("/journal/" + seg(date))
}
func (c *Client) SaveJournalSummary(date, summary string) (any, error) {
	return c.Request("POST", "/journal/"+seg(date)+"/summary", js.O("agent_summary", summary))
}

// Datasheets (called "tables" on the wire: /api/v1/tables…). Every one of
// these 404s in a workspace without the feature flag — the same answer as a
// datasheet that does not exist, so the module's existence doesn't leak.

func (c *Client) ListTables(p Params) (any, error)  { return c.Get("/tables" + qs(p)) }
func (c *Client) GetTable(id string) (any, error)   { return c.Get("/tables/" + seg(id)) }
func (c *Client) CreateTable(body any) (any, error) { return c.Request("POST", "/tables", body) }
func (c *Client) UpdateTable(id string, body any) (any, error) {
	return c.Request("PATCH", "/tables/"+seg(id), body)
}
func (c *Client) DeleteTable(id string) (any, error) {
	return c.Request("DELETE", "/tables/"+seg(id), nil)
}
func (c *Client) AddField(tableID string, body any) (any, error) {
	return c.Request("POST", "/tables/"+seg(tableID)+"/fields", body)
}
func (c *Client) UpdateField(tableID, fieldID string, body any) (any, error) {
	return c.Request("PATCH", "/tables/"+seg(tableID)+"/fields/"+seg(fieldID), body)
}
func (c *Client) DeleteField(tableID, fieldID string) (any, error) {
	return c.Request("DELETE", "/tables/"+seg(tableID)+"/fields/"+seg(fieldID), nil)
}

// ListRows is one page of rows, exactly as asked for.
func (c *Client) ListRows(tableID string, p Params) (any, error) {
	return c.Get("/tables/" + seg(tableID) + "/rows" + qs(p))
}

// ListAllRows returns every row matching the query, paged: the route caps
// `limit` at 200, so a single call would silently answer with the first 200
// rows — a truncated list and a complete one look identical to the reader. The
// order is deterministic (chosen sort, then `id`), which is what makes offset
// paging safe; rows are still deduped by id because a row inserted mid-walk
// shifts the boundary and hands the same row to two pages. `maxPages` is a
// fan-out backstop, not a display limit — when it bites, `meta.truncated` says
// so instead of the answer just ending.
func (c *Client) ListAllRows(tableID string, p Params, maxPages int) (any, error) {
	const page = 200
	seen := map[string]bool{}
	data := []any{}
	// The offset counts the rows the SERVER handed over, not the ones that
	// survived the dedupe — deriving it from len(data) re-asks for the same
	// page forever as soon as one duplicate is dropped.
	fetched := 0
	matched := 0.0
	for i := 0; ; i++ {
		if i >= maxPages {
			return js.O("data", data, "meta", js.O("total", len(data), "matched", matched, "truncated", true)), nil
		}
		q := js.NewObject()
		for _, k := range p.Keys() {
			v, _ := p.Get(k)
			q.Set(k, v)
		}
		q.Set("limit", page)
		q.Set("offset", fetched)
		res, err := c.ListRows(tableID, q)
		if err != nil {
			return nil, err
		}
		rows := js.List(js.Get(res, "data"))
		matched = js.Number(js.Or(js.Get(res, "meta", "total"), 0.0))
		fetched += len(rows)
		for _, r := range rows {
			if id := js.Get(r, "id"); js.Truthy(id) {
				key := js.Str(id)
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			data = append(data, r)
		}
		if len(rows) < page || float64(fetched) >= matched {
			break
		}
	}
	return js.O("data", data, "meta", js.O("total", len(data), "matched", matched, "truncated", false)), nil
}

func (c *Client) CreateRows(tableID string, rows []any) (any, error) {
	wrapped := make([]any, len(rows))
	for i, r := range rows {
		wrapped[i] = js.O("data", r)
	}
	return c.Request("POST", "/tables/"+seg(tableID)+"/rows", js.O("rows", wrapped))
}
func (c *Client) UpdateRows(tableID string, rows []any) (any, error) {
	return c.Request("PATCH", "/tables/"+seg(tableID)+"/rows", js.O("rows", rows))
}
func (c *Client) DeleteRows(tableID string, ids []string) (any, error) {
	return c.Request("DELETE", "/tables/"+seg(tableID)+"/rows", js.O("ids", ids))
}
func (c *Client) CreateTableComment(tableID, body string) (any, error) {
	return c.Request("POST", "/tables/"+seg(tableID)+"/comments", js.O("body", body))
}
