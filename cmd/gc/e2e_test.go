// End-to-end: the real gc binary (built once in TestMain, run as a separate
// process) against a small mock of the GROUNDCONTROL API.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var gcBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gc-bin-")
	if err != nil {
		panic(err)
	}
	gcBin = filepath.Join(dir, "gc")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=9.9.9-test", "-o", gcBin, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("building gc: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	out, err string
	code     int
}

// runGc runs the binary with a minimal environment (PATH, HOME, GC_CONFIG_DIR + extra).
func runGc(t *testing.T, home string, args []string, stdin *string, extra map[string]string) result {
	t.Helper()
	cmd := exec.Command(gcBin, args...)
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GC_CONFIG_DIR=" + filepath.Join(home, "config")}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = strings.NewReader(*stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running gc: %v", err)
	}
	return result{out.String(), errb.String(), code}
}

func strp(s string) *string { return &s }

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected output to contain %q, got:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("output must not contain %q, got:\n%s", needle, haystack)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

const (
	key       = "gc_live_e2eSecretKey0000abcd"
	taskID    = "11111111-2222-3333-4444-555555555555"
	checkedAt = "2026-10-02T12:00:05.000Z"
	cursorVal = "2026-10-02T12:00:00.000Z"
)

type recorded struct {
	method, path string
	query        url.Values
	auth         string
	session      string
	contentType  string
	body         any
	raw          []byte
}

type mockAPI struct {
	mu     sync.Mutex
	reqs   []recorded
	cursor string // "" = omit meta.cursor
	srv    *httptest.Server
	base   string
}

func (m *mockAPI) last() recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reqs[len(m.reqs)-1]
}

func (m *mockAPI) lastPath(p string) recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.reqs) - 1; i >= 0; i-- {
		if m.reqs[i].path == p {
			return m.reqs[i]
		}
	}
	return recorded{}
}

func (m *mockAPI) lastMethod(method string) recorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.reqs) - 1; i >= 0; i-- {
		if m.reqs[i].method == method {
			return m.reqs[i]
		}
	}
	return recorded{}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func newMockAPI() *mockAPI {
	m := &mockAPI{cursor: cursorVal}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body any
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && len(raw) > 0 {
			json.Unmarshal(raw, &body)
		}
		m.mu.Lock()
		m.reqs = append(m.reqs, recorded{r.Method, r.URL.Path, r.URL.Query(), r.Header.Get("Authorization"), r.Header.Get("X-GC-Session-Id"), r.Header.Get("Content-Type"), body, raw})
		cursor := m.cursor
		m.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/api/v1")
		if p == "/guide" && r.Method == "GET" {
			writeJSON(w, 200, `{"data":[{"topic":"start","title":"Start here","summary":"First steps"}]}`)
			return
		}
		if p == "/guide/tasks" {
			writeJSON(w, 200, `{"data":{"topic":"tasks","title":"Tasks","body":"# Tasks\n\nUse gc tasks."}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			writeJSON(w, 401, `{"error":{"code":"unauthorized","message":"Invalid API key"}}`)
			return
		}
		b, _ := body.(map[string]any)
		merge := func(base map[string]any) string {
			for k, v := range b {
				base[k] = v
			}
			out, _ := json.Marshal(base)
			return string(out)
		}
		switch {
		case p == "/me":
			writeJSON(w, 200, `{"data":{"user":{"id":"m1","display_name":"Robo","is_agent":true,"role":"member"},"tenant":{"id":"t1","name":"Acme Labs","slug":"acme","workflow":"scrum","features":{"datasheets":true,"desk":false}},"tenants":[]}}`)
		case p == "/tasks" && r.Method == "GET":
			writeJSON(w, 200, `{"data":[{"id":"`+taskID+`","title":"Ship the CLI","status":"in_progress","priority":"high","assignee":{"display_name":"Robo"},"initiative":{"name":"Core"}}],"meta":{"total":1,"limit":100,"offset":0}}`)
		case p == "/tasks" && r.Method == "POST":
			writeJSON(w, 201, `{"data":`+merge(map[string]any{"id": taskID, "status": "todo", "priority": "medium"})+`}`)
		case p == "/tasks/"+taskID && r.Method == "PATCH":
			warnings := ""
			if b["status"] == "done" {
				warnings = `,"warnings":[{"code":"scrum_expects_review","message":"finish with review"}]`
			}
			writeJSON(w, 200, `{"data":`+merge(map[string]any{"id": taskID, "title": "Ship the CLI", "status": "review", "priority": "high"})+warnings+`}`)
		case p == "/tasks/"+taskID:
			writeJSON(w, 200, `{"data":{"id":"`+taskID+`","title":"Ship the CLI","status":"in_progress","priority":"high","description":"Build **gc**.","assignee":{"display_name":"Robo"},"initiative":{"name":"Core"},"updated_at":"2026-10-02T09:00:00Z"}}`)
		case p == "/tasks/"+taskID+"/comments" && r.Method == "GET":
			writeJSON(w, 200, `{"data":[{"id":"c1","author":{"display_name":"Chris"},"body":"Old comment","created_at":"2026-10-01T08:00:00Z"},{"id":"c2","author":{"display_name":"Chris"},"body":"New comment","created_at":"2026-10-02T11:00:00Z"}],"meta":{"total":2}}`)
		case p == "/tasks/"+taskID+"/comments" && r.Method == "POST":
			out, _ := json.Marshal(map[string]any{"data": map[string]any{"id": "c3", "body": b["body"]}})
			writeJSON(w, 201, string(out))
		case p == "/tasks/"+taskID+"/attachments" && r.Method == "POST":
			writeJSON(w, 201, `{"data":{"id":"a1","file_name":"notes.md","file_size":2048}}`)
		case p == "/tasks/"+taskID+"/attachments":
			writeJSON(w, 200, `{"data":[]}`)
		case p == "/changes":
			meta := fmt.Sprintf(`"meta":{"since":%q,"checked_at":%q`, r.URL.Query().Get("since"), checkedAt)
			if cursor != "" {
				meta += fmt.Sprintf(`,"cursor":%q`, cursor)
			}
			meta += "}"
			writeJSON(w, 200, `{"data":{"task_comments":[{"id":"c2","task_id":"`+taskID+`","content":"Please also add docs","created_at":"2026-10-02T11:00:00Z","task_title":"Ship the CLI","for":"self"}],"doc_comments":[],"tasks_created":[],"docs_updated":[],"goal_checkins":[],"tasks_updated":[{"id":"x2","title":"Book flights","status":"todo","priority":"medium","for":"principal"}]},`+meta+`}`)
		default:
			writeJSON(w, 404, `{"error":{"code":"not_found","message":"Not found"}}`)
		}
	}))
	m.base = m.srv.URL + "/api/v1"
	return m
}

func TestE2E(t *testing.T) {
	api := newMockAPI()
	defer api.srv.Close()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "config", "config.json")
	gc := func(args []string, stdin *string, env map[string]string) result {
		return runGc(t, home, args, stdin, env)
	}

	t.Run("onboarding reads the key from stdin, verifies it, stores it 0600, never prints it", func(t *testing.T) {
		r := gc([]string{"onboarding", "--token", "-", "--api-url", api.base}, strp(key+"\n"), nil)
		if r.err != "" || r.code != 0 {
			t.Fatalf("code %d, stderr %q", r.code, r.err)
		}
		mustContain(t, r.out, `Connected to workspace "Acme Labs" as "Robo" (scrum)`)
		mustContain(t, r.out, "Profile: acme-labs (current)")
		mustNotContain(t, r.out+r.err, key)
		mustContain(t, r.out, "gc_live_…abcd")
		if m := fileMode(t, cfgPath); m != 0o600 {
			t.Fatalf("config mode %o", m)
		}
		if m := fileMode(t, filepath.Dir(cfgPath)); m != 0o700 {
			t.Fatalf("config dir mode %o", m)
		}
		cfg := readJSON(t, cfgPath)
		if cfg["current"] != "acme-labs" {
			t.Fatalf("current = %v", cfg["current"])
		}
		p := cfg["profiles"].(map[string]any)["acme-labs"].(map[string]any)
		if p["api_url"] != api.base || p["api_key"] != key || p["workspace"] != "Acme Labs" || p["agent"] != "Robo" {
			t.Fatalf("profile = %v", p)
		}
		if api.last().path != "/api/v1/me" {
			t.Fatalf("last request %s", api.last().path)
		}
	})

	t.Run("onboarding with a bad key fails (exit 3) and saves nothing new", func(t *testing.T) {
		r := gc([]string{"onboarding", "--token", "gc_live_wrongwrongwrong", "--api-url", api.base, "--profile", "bad"}, nil, nil)
		if r.code != 3 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "401")
		mustNotContain(t, r.err, "gc_live_wrongwrongwrong")
		if _, ok := readJSON(t, cfgPath)["profiles"].(map[string]any)["bad"]; ok {
			t.Fatal("bad profile was saved")
		}
	})

	t.Run("context: identity, workflow rule, open tasks; session id header", func(t *testing.T) {
		r := gc([]string{"context"}, nil, map[string]string{"GC_SESSION_ID": "sess-42"})
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.out, `Robo (agent, member) in workspace "Acme Labs" · profile acme-labs`)
		mustContain(t, r.out, "scrum — finish your tasks with status review")
		mustContain(t, r.out, "Features: datasheets")
		mustContain(t, r.out, taskID+"  in_progress  high      Ship the CLI  @Robo  #Core")
		list := api.lastPath("/api/v1/tasks")
		if list.query.Get("assigned_to") != "me" || list.query.Get("status") != "backlog,scheduled,todo,in_progress,blocked,review" {
			t.Fatalf("query %v", list.query)
		}
		if list.session != "sess-42" {
			t.Fatalf("session header %q", list.session)
		}
		if list.auth != "Bearer "+key {
			t.Fatal("no auth header")
		}
	})

	t.Run("context --field and --json keep the server's key order", func(t *testing.T) {
		r := gc([]string{"context", "--field", "me.tenant.workflow"}, nil, nil)
		if r.out != "scrum\n" {
			t.Fatalf("field: %q", r.out)
		}
		r = gc([]string{"context", "--json"}, nil, nil)
		if !strings.Contains(r.out, "\"tenant\": {\n      \"id\": \"t1\",\n      \"name\": \"Acme Labs\",") {
			t.Fatalf("json layout/order:\n%s", r.out)
		}
	})

	t.Run("tasks get: description + comments; --since only new; --field; --json", func(t *testing.T) {
		r := gc([]string{"tasks", "get", taskID}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.out, "# Ship the CLI")
		mustContain(t, r.out, taskID+" · in_progress · high · @Robo · #Core · updated 2026-10-02 09:00Z")
		mustContain(t, r.out, "Build **gc**.")
		mustContain(t, r.out, "## Comments (2)")
		mustContain(t, r.out, "--- Chris · 2026-10-01 08:00Z\nOld comment")
		r = gc([]string{"tasks", "get", taskID, "--since", "2026-10-02T00:00:00Z"}, nil, nil)
		mustContain(t, r.out, "## Comments (1 new since 2026-10-02 00:00Z, 2 total)")
		mustNotContain(t, r.out, "Old comment")
		r = gc([]string{"tasks", "get", taskID, "--comments", "new"}, nil, nil)
		if r.code != 2 {
			t.Fatalf("--comments new without --since: code %d", r.code)
		}
		r = gc([]string{"tasks", "get", taskID, "--comments", "none"}, nil, nil)
		mustNotContain(t, r.out, "## Comments")
		r = gc([]string{"tasks", "get", taskID, "--field", "description"}, nil, nil)
		if r.out != "Build **gc**.\n" {
			t.Fatalf("field: %q", r.out)
		}
		r = gc([]string{"tasks", "get", taskID, "--json"}, nil, nil)
		var parsed struct {
			Data struct {
				Comments    []any `json:"comments"`
				Attachments []any `json:"attachments"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(r.out), &parsed); err != nil || len(parsed.Data.Comments) != 2 || parsed.Data.Attachments == nil {
			t.Fatalf("json: %v %s", err, r.out)
		}
	})

	t.Run("comment --body-file - posts stdin Markdown verbatim", func(t *testing.T) {
		md := "## Done\n\n- PR: https://example.com/pr/1\n- `code`\n"
		r := gc([]string{"comment", taskID, "--body-file", "-"}, strp(md), nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.out, "commented on "+taskID+" (c3)")
		post := api.lastMethod("POST")
		if b := post.body.(map[string]any); b["body"] != strings.TrimRight(md, "\n") || len(b) != 1 {
			t.Fatalf("body %v", post.body)
		}
		r = gc([]string{"comment", taskID, "  "}, nil, nil)
		if r.code != 2 {
			t.Fatalf("empty body: code %d", r.code)
		}
	})

	t.Run("attach: multipart with the content type from the extension", func(t *testing.T) {
		f := filepath.Join(home, "notes.md")
		os.WriteFile(f, []byte("# notes\n"), 0o644)
		r := gc([]string{"attach", taskID, f}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.out, "attached notes.md (2.0 KB) to "+taskID)
		req := api.lastMethod("POST")
		if !strings.HasPrefix(req.contentType, "multipart/form-data; boundary=") {
			t.Fatalf("content type %q", req.contentType)
		}
		mustContain(t, string(req.raw), `Content-Disposition: form-data; name="file"; filename="notes.md"`)
		mustContain(t, string(req.raw), "Content-Type: text/markdown")
		mustContain(t, string(req.raw), "# notes\n")
		r = gc([]string{"attach", taskID, filepath.Join(home, "nope.txt")}, nil, nil)
		if r.code != 1 {
			t.Fatalf("missing file: code %d", r.code)
		}
	})

	t.Run("changes --cursor-file: missing file → 1h default; writes the server cursor; reuses it", func(t *testing.T) {
		cursor := filepath.Join(home, "state", "cursor")
		before := time.Now()
		r := gc([]string{"changes", "--cursor-file", cursor}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.err, "does not exist yet")
		mustContain(t, r.out, "Please also add docs")
		mustContain(t, r.out, "[principal]")
		mustContain(t, r.out, "do NOT start it")
		since1, _ := time.Parse(time.RFC3339Nano, api.lastPath("/api/v1/changes").query.Get("since"))
		if d := since1.Sub(before.Add(-time.Hour)); d > 10*time.Second || d < -10*time.Second {
			t.Fatalf("since %v is not ~1h ago", since1)
		}
		b, _ := os.ReadFile(cursor)
		if strings.TrimSpace(string(b)) != cursorVal { // meta.cursor, not checked_at, not the local clock
			t.Fatalf("cursor file %q", b)
		}
		if m := fileMode(t, cursor); m != 0o600 {
			t.Fatalf("cursor mode %o", m)
		}

		r = gc([]string{"changes", "--cursor-file", cursor}, nil, nil)
		if r.code != 0 || api.lastPath("/api/v1/changes").query.Get("since") != cursorVal {
			t.Fatalf("second poll since %q", api.lastPath("/api/v1/changes").query.Get("since"))
		}

		// older server without meta.cursor → checked_at
		api.mu.Lock()
		api.cursor = ""
		api.mu.Unlock()
		gc([]string{"changes", "--cursor-file", cursor}, nil, nil)
		api.mu.Lock()
		api.cursor = cursorVal
		api.mu.Unlock()
		b, _ = os.ReadFile(cursor)
		if strings.TrimSpace(string(b)) != checkedAt {
			t.Fatalf("cursor file %q, want checked_at", b)
		}

		// --since wins over the file
		gc([]string{"changes", "--since", "2026-10-02T06:00:00Z", "--cursor-file", cursor}, nil, nil)
		if s := api.lastPath("/api/v1/changes").query.Get("since"); s != "2026-10-02T06:00:00.000Z" {
			t.Fatalf("since %q", s)
		}
	})

	t.Run("changes --cursor-file: output fails (bad --field) → cursor not advanced", func(t *testing.T) {
		cursor := filepath.Join(home, "state", "cursor-keep")
		os.MkdirAll(filepath.Dir(cursor), 0o755)
		os.WriteFile(cursor, []byte("2026-10-01T00:00:00.000Z\n"), 0o600)
		r := gc([]string{"changes", "--cursor-file", cursor, "--field", "data.no_such_list"}, nil, nil)
		if r.code == 0 {
			t.Fatal("expected a failure")
		}
		b, _ := os.ReadFile(cursor)
		if strings.TrimSpace(string(b)) != "2026-10-01T00:00:00.000Z" {
			t.Fatalf("cursor advanced to %q", b)
		}
	})

	t.Run("changes --cursor-file: a cursor file with garbage is an error", func(t *testing.T) {
		cursor := filepath.Join(home, "state", "cursor-bad")
		os.WriteFile(cursor, []byte("yesterday\n"), 0o600)
		r := gc([]string{"changes", "--cursor-file", cursor}, nil, nil)
		if r.code != 1 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "does not hold a timestamp")
	})

	t.Run("changes without cursor file stores nothing anywhere", func(t *testing.T) {
		r := gc([]string{"changes", "--since", "15m"}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d", r.code)
		}
		entries, _ := os.ReadDir(filepath.Join(home, "config"))
		if len(entries) != 1 {
			t.Fatalf("config dir holds %v", entries)
		}
	})

	t.Run("tasks create/update: empty flags are not sent, none clears, warnings go to stderr", func(t *testing.T) {
		r := gc([]string{"tasks", "create", "--title", "New one", "--priority", "", "--description-file", "-", "--story-points", "3"}, strp("Desc **md**\n"), nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		want := `{"description":"Desc **md**\n","story_points":3,"title":"New one"}`
		if got, _ := json.Marshal(api.last().body); string(got) != want {
			t.Fatalf("create body %s", got)
		}
		r = gc([]string{"tasks", "update", taskID, "--assign", "none", "--status", "done", "--due", ""}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		if got, _ := json.Marshal(api.last().body); string(got) != `{"assigned_to":null,"status":"done"}` {
			t.Fatalf("update body %s", got)
		}
		mustContain(t, r.err, "warning: scrum_expects_review — finish with review")
		if r := gc([]string{"tasks", "update", taskID}, nil, nil); r.code != 2 {
			t.Fatalf("empty update: code %d", r.code)
		}
		if r := gc([]string{"tasks", "create", "--story-points", "many", "--title", "x"}, nil, nil); r.code != 2 {
			t.Fatalf("bad number: code %d", r.code)
		}
		if r := gc([]string{"tasks", "create", "--priority", "high"}, nil, nil); r.code != 2 {
			t.Fatalf("missing title: code %d", r.code)
		}
	})

	t.Run("global flags anywhere: before the command and after positionals", func(t *testing.T) {
		r := gc([]string{"--profile", "acme-labs", "tasks", "get", taskID, "--field", "title"}, nil, nil)
		if r.out != "Ship the CLI\n" {
			t.Fatalf("got %q (%s)", r.out, r.err)
		}
		r = gc([]string{"--json", "tasks", "list"}, nil, nil)
		if !strings.HasPrefix(r.out, "{\n  \"data\": [") {
			t.Fatalf("got %q", r.out)
		}
		// --json asks for the full rows: no fields=summary
		if f := api.lastPath("/api/v1/tasks").query.Get("fields"); f != "" {
			t.Fatalf("fields=%q with --json", f)
		}
	})

	t.Run("guide lists topics and prints a body raw, without a key", func(t *testing.T) {
		r := gc([]string{"guide"}, nil, nil)
		mustContain(t, r.out, "start  Start here — First steps")
		mustContain(t, r.out, "Read one: gc guide <topic>")
		r = gc([]string{"guide", "tasks"}, nil, nil)
		if r.out != "# Tasks\n\nUse gc tasks.\n" {
			t.Fatalf("got %q", r.out)
		}
		empty := t.TempDir()
		r = runGc(t, empty, []string{"guide", "tasks", "--api-url", api.base}, nil, nil)
		if r.code != 0 || r.out != "# Tasks\n\nUse gc tasks.\n" {
			t.Fatalf("keyless guide: code %d %q %s", r.code, r.out, r.err)
		}
		if a := api.lastPath("/api/v1/guide/tasks").auth; a != "" {
			t.Fatalf("keyless guide sent auth %q", a)
		}
	})

	t.Run("API errors exit 1; a rejected key exits 3; GC_API_KEY overrides the profile", func(t *testing.T) {
		r := gc([]string{"tasks", "get", "does-not-exist"}, nil, nil)
		if r.code != 1 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "gc: GROUNDCONTROL API error 404: Not found")
		r = gc([]string{"context"}, nil, map[string]string{"GC_API_KEY": "gc_live_overrideoverride"})
		if r.code != 3 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "not connected on this machine (the API key was rejected: 401)")
		mustContain(t, r.err, "registers at "+api.srv.URL+" ")
		mustNotContain(t, r.err, "gc_live_overrideoverride")
		r = gc([]string{"tables", "get", "nope"}, nil, nil)
		if r.code != 1 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "HTTP 404 = no such datasheet")
	})

	t.Run("not connected and usage errors", func(t *testing.T) {
		empty := t.TempDir()
		r := runGc(t, empty, []string{"context"}, nil, nil)
		if r.code != 3 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "GROUNDCONTROL is not connected on this machine (no API key)")
		mustContain(t, r.err, "registers at https://groundcontrol.makerslab.ai")
		mustContain(t, r.err, `gc onboarding --token "gc_live_…"`)
		r = runGc(t, empty, []string{"tasks", "list"}, nil, map[string]string{"GC_API_URL": "http://localhost:3011/api/v1"})
		if r.code != 3 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "registers at http://localhost:3011 ")
		for _, args := range [][]string{{"tasks", "list", "--bogus"}, {"comment", "id", "a", "b"}, {"tasks", "get", "x", "--json", "--field", "a"}, {"frobnicate"}, {"tasks", "frobnicate"}} {
			if r := runGc(t, empty, args, nil, nil); r.code != 2 {
				t.Fatalf("%v: code %d", args, r.code)
			}
		}
	})

	t.Run("version and help", func(t *testing.T) {
		if r := gc([]string{"version"}, nil, nil); r.out != "gc 9.9.9-test\n" {
			t.Fatalf("version %q", r.out)
		}
		if r := gc([]string{"--version"}, nil, nil); r.out != "gc 9.9.9-test\n" {
			t.Fatalf("--version %q", r.out)
		}
		if r := gc([]string{"version", "--json"}, nil, nil); r.out != "{\n  \"version\": \"9.9.9-test\"\n}\n" {
			t.Fatalf("version --json %q", r.out)
		}
		r := gc(nil, nil, nil)
		mustContain(t, r.out, "gc 9.9.9-test — GROUNDCONTROL for agents (alias: groundcontrol)")
		if r := gc([]string{"tasks"}, nil, nil); r.code != 0 {
			t.Fatalf("group help code %d", r.code)
		}
	})
}
