// `gc listen` end-to-end: the real binary against a mock with two workspaces.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	keyA = "gc_live_listenWorkspaceA0001"
	keyB = "gc_live_listenWorkspaceB0002"
)

var listenCursors = map[string]string{keyA: "2026-10-02T12:00:00.000Z", keyB: "2026-10-02T12:30:00.000Z"}

type poll struct{ key, since string }

type listenMock struct {
	stall bool // hold /changes until the client goes away
	mu    sync.Mutex
	items map[string]bool
	polls []poll
	srv   *httptest.Server
	base  string
}

func (m *listenMock) snapshot() []poll {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]poll(nil), m.polls...)
}

func newListenMock() *listenMock {
	m := &listenMock{items: map[string]bool{keyA: true, keyB: false}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		cursor, ok := listenCursors[k]
		if !ok {
			writeJSON(w, 401, `{"error":{"code":"unauthorized","message":"Invalid API key"}}`)
			return
		}
		if r.URL.Path != "/api/v1/changes" {
			writeJSON(w, 404, `{"error":{"message":"Not found"}}`)
			return
		}
		since := r.URL.Query().Get("since")
		m.mu.Lock()
		m.polls = append(m.polls, poll{k, since})
		has := m.items[k]
		stall := m.stall
		m.mu.Unlock()
		if stall {
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
			return
		}
		data := `{"task_comments":[],"tasks_updated":[],"goal_checkins":[]}`
		if has {
			data = `{"task_comments":[{"id":"c1","task_id":"t1","task_title":"Ship","content":"hi","created_at":"2026-10-02T11:59:00Z","for":"self"}],` +
				`"tasks_updated":[{"id":"t2","title":"Book flights","updated_at":"2026-10-02T11:58:00Z","for":"principal"}],` +
				`"some_future_kind":[{"id":"f1","created_at":"2026-10-02T11:57:00Z"}],"goal_checkins":[]}`
		}
		writeJSON(w, 200, fmt.Sprintf(`{"data":%s,"meta":{"since":%q,"cursor":%q,"checked_at":"2026-10-02T13:00:00.000Z"}}`, data, since, cursor))
	}))
	m.base = m.srv.URL + "/api/v1"
	return m
}

// listenCase is one fresh config dir with profiles alpha (A) and beta (B).
func listenCase(t *testing.T, m *listenMock) string {
	t.Helper()
	m.mu.Lock()
	m.polls = nil
	m.items = map[string]bool{keyA: true, keyB: false}
	m.stall = false
	m.mu.Unlock()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "config"), 0o700)
	writeConfig(t, home, m.base, keyA, keyB)
	return home
}

func writeConfig(t *testing.T, home, base, a, b string) {
	t.Helper()
	profile := func(k string) string {
		return fmt.Sprintf(`{"api_url":%q,"api_key":%q,"workspace":%q,"agent":"Robo","created_at":"2026-10-01T00:00:00Z"}`, base, k, k[len(k)-5:])
	}
	cfg := fmt.Sprintf(`{"current":"alpha","profiles":{"alpha":%s,"beta":%s}}`, profile(a), profile(b))
	if err := os.WriteFile(filepath.Join(home, "config", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cursorOf(t *testing.T, home, name string) string {
	t.Helper()
	m := readJSON(t, filepath.Join(home, "config", "listen", name+".json"))
	return m["cursor"].(string)
}

func TestListen(t *testing.T) {
	m := newListenMock()
	defer m.srv.Close()

	t.Run("--once: first run starts at now, JSON lines for every array, cursor = meta.cursor; next run resumes", func(t *testing.T) {
		home := listenCase(t, m)
		before := time.Now()
		r := runGc(t, home, []string{"listen", "--once", "--json"}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		since0, _ := time.Parse(time.RFC3339Nano, m.snapshot()[0].since)
		if d := since0.Sub(before); d > 10*time.Second || d < -10*time.Second {
			t.Fatalf("first since %v, want ~now", since0)
		}
		lines := strings.Split(strings.TrimSpace(r.out), "\n")
		kinds := []string{}
		var first, second map[string]any
		for i, l := range lines {
			var x map[string]any
			if err := json.Unmarshal([]byte(l), &x); err != nil {
				t.Fatalf("line %q: %v", l, err)
			}
			kinds = append(kinds, x["kind"].(string))
			if i == 0 {
				first = x
			}
			if i == 1 {
				second = x
			}
		}
		if strings.Join(kinds, ",") != "task_comments,tasks_updated,some_future_kind" {
			t.Fatalf("kinds %v", kinds)
		}
		if lines[0] != `{"profile":"alpha","kind":"task_comments","for":"self","id":"c1","created_at":"2026-10-02T11:59:00Z","task_id":"t1","title":"Ship"}` {
			t.Fatalf("item line %s", lines[0])
		}
		if first["task_id"] != "t1" || second["for"] != "principal" {
			t.Fatalf("items %v %v", first, second)
		}
		mustContain(t, r.err, "[alpha] 3 changes (task_comments 1, tasks_updated 1, some_future_kind 1)")
		if c := cursorOf(t, home, "alpha"); c != listenCursors[keyA] {
			t.Fatalf("cursor %s", c)
		}
		if mode := fileMode(t, filepath.Join(home, "config", "listen", "alpha.json")); mode != 0o600 {
			t.Fatalf("cursor mode %o", mode)
		}

		r = runGc(t, home, []string{"listen", "--once"}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d", r.code)
		}
		if p := m.snapshot()[1]; p.since != listenCursors[keyA] {
			t.Fatalf("second since %s", p.since)
		}
		mustContain(t, r.out, "alpha  task_comments  c1  task t1")
		mustContain(t, r.out, "alpha  tasks_updated  [principal] t2")
	})

	t.Run("--since overrides the stored cursor", func(t *testing.T) {
		home := listenCase(t, m)
		runGc(t, home, []string{"listen", "--once", "--since", "2026-10-01T00:00:00Z"}, nil, nil)
		if p := m.snapshot()[0]; p.since != "2026-10-01T00:00:00.000Z" {
			t.Fatalf("since %s", p.since)
		}
	})

	t.Run("--all-profiles: separate cursors per workspace", func(t *testing.T) {
		home := listenCase(t, m)
		r := runGc(t, home, []string{"listen", "--once", "--all-profiles"}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		ps := m.snapshot()
		if len(ps) != 2 || ps[0].key != keyA || ps[1].key != keyB {
			t.Fatalf("polls %v", ps)
		}
		if cursorOf(t, home, "alpha") != listenCursors[keyA] || cursorOf(t, home, "beta") != listenCursors[keyB] {
			t.Fatal("cursors not per workspace")
		}
		mustContain(t, r.err, "[beta] 0 changes")
		runGc(t, home, []string{"listen", "--once", "--all-profiles"}, nil, nil)
		ps = m.snapshot()
		if ps[2] != (poll{keyA, listenCursors[keyA]}) || ps[3] != (poll{keyB, listenCursors[keyB]}) {
			t.Fatalf("resumed polls %v", ps[2:])
		}
	})

	t.Run("--exec: runs only when there are changes, with the workspace env; non-zero exit still advances", func(t *testing.T) {
		home := listenCase(t, m)
		envOut := filepath.Join(home, "env.txt")
		runs := filepath.Join(home, "runs.txt")
		cmd := fmt.Sprintf(`printf '%%s\n' "$GC_PROFILE" "$GC_API_KEY" "$GC_API_URL" "$GC_LISTEN_SINCE" "$GC_LISTEN_COUNT" > %s; cat "$GC_LISTEN_CHANGES_FILE" >> %s; echo "ran $GC_PROFILE" >> %s; exit 7`, envOut, envOut, runs)
		r := runGc(t, home, []string{"listen", "--once", "--all-profiles", "--since", "2026-10-02T10:00:00Z", "--exec", cmd}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		b, _ := os.ReadFile(envOut)
		parts := strings.SplitN(string(b), "\n", 6)
		if got := strings.Join(parts[:5], "|"); got != strings.Join([]string{"alpha", keyA, m.base, "2026-10-02T10:00:00.000Z", "3"}, "|") {
			t.Fatalf("exec env %s", got)
		}
		var changes struct {
			Meta struct{ Cursor string } `json:"meta"`
		}
		if err := json.Unmarshal([]byte(parts[5]), &changes); err != nil || changes.Meta.Cursor != listenCursors[keyA] {
			t.Fatalf("changes file: %v %q", err, parts[5])
		}
		// beta had no changes → no exec
		if b, _ := os.ReadFile(runs); string(b) != "ran alpha\n" {
			t.Fatalf("runs %q", b)
		}
		mustContain(t, r.err, "exec exited with 7 — cursor advanced anyway")
		if cursorOf(t, home, "alpha") != listenCursors[keyA] {
			t.Fatal("cursor not advanced")
		}
		mustNotContain(t, r.err, keyA)
	})

	t.Run("a key from GC_API_KEY gets a hashed cursor name and no GC_PROFILE in the exec env", func(t *testing.T) {
		home := listenCase(t, m)
		out := filepath.Join(home, "p.txt")
		r := runGc(t, home, []string{"listen", "--once", "--exec", fmt.Sprintf(`printf '[%%s]' "${GC_PROFILE-unset}" > %s`, out)}, nil,
			map[string]string{"GC_API_KEY": keyA, "GC_API_URL": m.base, "GC_PROFILE": "alpha"})
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		if b, _ := os.ReadFile(out); string(b) != "[unset]" {
			t.Fatalf("GC_PROFILE in exec env: %q", b)
		}
		if !regexp.MustCompile(`\[key-[0-9a-f]{12}\]`).MatchString(r.err) {
			t.Fatalf("no hashed name in %s", r.err)
		}
	})

	t.Run("401: one rejected profile is logged and the others continue; all rejected → exit 3", func(t *testing.T) {
		home := listenCase(t, m)
		writeConfig(t, home, m.base, keyA, "gc_live_revokedrevoked0000")
		r := runGc(t, home, []string{"listen", "--once", "--all-profiles"}, nil, nil)
		if r.code != 0 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.err, "[beta] API key rejected (401)")
		if cursorOf(t, home, "alpha") != listenCursors[keyA] {
			t.Fatal("alpha cursor")
		}
		writeConfig(t, home, m.base, "gc_live_revokedrevoked1111", "gc_live_revokedrevoked0000")
		r = runGc(t, home, []string{"listen", "--once", "--all-profiles"}, nil, nil)
		if r.code != 3 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "GROUNDCONTROL is not connected on this machine (the API key was rejected: 401)")
	})

	t.Run("no key at all → exit 3", func(t *testing.T) {
		home := listenCase(t, m)
		os.WriteFile(filepath.Join(home, "config", "config.json"), []byte(`{"current":null,"profiles":{}}`), 0o600)
		r := runGc(t, home, []string{"listen", "--once"}, nil, nil)
		if r.code != 3 {
			t.Fatalf("code %d", r.code)
		}
		mustContain(t, r.err, "(no API key)")
	})

	t.Run("loop mode stops cleanly on SIGTERM", func(t *testing.T) {
		home := listenCase(t, m)
		m.mu.Lock()
		m.items[keyA] = false
		m.mu.Unlock()
		cmd := exec.Command(gcBin, "listen", "--interval", "5s")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GC_CONFIG_DIR=" + filepath.Join(home, "config")}
		var errb bytes.Buffer
		cmd.Stderr = &errb
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(m.snapshot()) == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if len(m.snapshot()) != 1 {
			t.Fatalf("polls %d", len(m.snapshot()))
		}
		time.Sleep(100 * time.Millisecond) // let it reach the sleep
		cmd.Process.Signal(syscall.SIGTERM)
		start := time.Now()
		err := cmd.Wait()
		if err != nil {
			t.Fatalf("exit: %v (%s)", err, errb.String())
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("did not stop promptly")
		}
		mustContain(t, errb.String(), "SIGTERM — stopping")
		if _, err := os.Stat(filepath.Join(home, "config", "listen", "alpha.json")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a corrupt cursor file fails that workspace (naming the file and --since); the others still poll", func(t *testing.T) {
		home := listenCase(t, m)
		bad := filepath.Join(home, "config", "listen", "alpha.json")
		os.MkdirAll(filepath.Dir(bad), 0o700)
		os.WriteFile(bad, []byte("{not json"), 0o600)
		r := runGc(t, home, []string{"listen", "--once", "--all-profiles"}, nil, nil)
		if r.code != 1 {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		mustContain(t, r.err, "cursor file "+bad+" is not valid JSON — pass --since")
		mustContain(t, r.err, "Poll failed for: alpha")
		if ps := m.snapshot(); len(ps) != 1 || ps[0].key != keyB {
			t.Fatalf("polls %v — alpha must not restart at now, beta must still poll", ps)
		}
		if b, _ := os.ReadFile(bad); string(b) != "{not json" {
			t.Fatal("the corrupt cursor was overwritten")
		}
		os.WriteFile(bad, []byte(`{"cursor":"soon"}`), 0o600)
		r = runGc(t, home, []string{"listen", "--once"}, nil, nil)
		if r.code != 1 || !strings.Contains(r.err, `does not hold a timestamp ("soon")`) {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
		// --since resets it
		r = runGc(t, home, []string{"listen", "--once", "--since", "1h"}, nil, nil)
		if r.code != 0 || cursorOf(t, home, "alpha") != listenCursors[keyA] {
			t.Fatalf("code %d: %s", r.code, r.err)
		}
	})

	t.Run("--all-profiles: a profile without api_url falls back to GC_API_URL", func(t *testing.T) {
		home := listenCase(t, m)
		os.WriteFile(filepath.Join(home, "config", "config.json"), []byte(`{"current":"alpha","profiles":{"alpha":{"api_key":"`+keyA+`"}}}`), 0o600)
		r := runGc(t, home, []string{"listen", "--once", "--all-profiles"}, nil, map[string]string{"GC_API_URL": m.base})
		if r.code != 0 || len(m.snapshot()) != 1 {
			t.Fatalf("code %d, polls %d: %s", r.code, len(m.snapshot()), r.err)
		}
	})

	t.Run("SIGTERM cancels an in-flight poll against a stalled server", func(t *testing.T) {
		home := listenCase(t, m)
		m.mu.Lock()
		m.stall = true
		m.mu.Unlock()
		cmd := exec.Command(gcBin, "listen", "--interval", "5s")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GC_CONFIG_DIR=" + filepath.Join(home, "config")}
		var errb bytes.Buffer
		cmd.Stderr = &errb
		cmd.Start()
		waitFor(t, func() bool { return len(m.snapshot()) == 1 })
		time.Sleep(100 * time.Millisecond)
		start := time.Now()
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			t.Fatalf("exit: %v (%s)", err, errb.String())
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("shutdown took %v", d)
		}
		mustContain(t, errb.String(), "poll cancelled")
	})

	t.Run("--exec runs in its own process group: SIGTERM reaches grandchildren; a second signal SIGKILLs", func(t *testing.T) {
		home := listenCase(t, m)
		pidFile := filepath.Join(home, "pid")
		start := func(script string) (*exec.Cmd, *bytes.Buffer) {
			cmd := exec.Command(gcBin, "listen", "--interval", "5s", "--exec", script)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GC_CONFIG_DIR=" + filepath.Join(home, "config")}
			var errb bytes.Buffer
			cmd.Stderr = &errb
			cmd.Start()
			waitFor(t, func() bool { b, _ := os.ReadFile(pidFile); return len(bytes.TrimSpace(b)) > 0 })
			return cmd, &errb
		}
		alive := func() bool {
			b, _ := os.ReadFile(pidFile)
			var pid int
			fmt.Sscan(string(b), &pid)
			return syscall.Kill(pid, 0) == nil
		}

		// graceful: the grandchild (sleep) is in the group and dies with it
		cmd, errb := start(fmt.Sprintf(`sleep 30 & echo $! > %s; wait`, pidFile))
		begin := time.Now()
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			t.Fatalf("exit: %v (%s)", err, errb.String())
		}
		// A surviving grandchild would hold our stderr pipe open for 30s.
		if d := time.Since(begin); d > 3*time.Second {
			t.Fatalf("graceful stop took %v — the grandchild outlived the command", d)
		}
		mustContain(t, errb.String(), "SIGTERM — stopping after the running command")
		waitFor(t, func() bool { return !alive() })

		// escalation: a command that ignores SIGTERM is killed by the second signal
		os.Remove(pidFile)
		m.mu.Lock()
		m.items[keyA] = true
		m.mu.Unlock()
		os.Remove(filepath.Join(home, "config", "listen", "alpha.json"))
		cmd, errb = start(fmt.Sprintf(`trap '' TERM; sleep 30 & echo $! > %s; wait; sleep 30`, pidFile))
		cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
		if !alive() {
			t.Fatal("the command should have ignored the first SIGTERM")
		}
		begin = time.Now()
		cmd.Process.Signal(syscall.SIGTERM)
		err := cmd.Wait()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 130 {
			t.Fatalf("exit %v, want 130 (%s)", err, errb.String())
		}
		if time.Since(begin) > 3*time.Second {
			t.Fatal("escalation was not prompt")
		}
		mustContain(t, errb.String(), "SIGTERM again — killing the running command")
		waitFor(t, func() bool { return !alive() })
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
