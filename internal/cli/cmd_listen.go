package cli

// `gc listen` — the successor of the OpenClaw plugin's gc-worker.sh: poll
// /changes per workspace (0 tokens) and wake an agent only when something
// happened. Each workspace has ONE cursor, owned by the listener alone
// (<config dir>/listen/<name>.json). It is never shared with a session's
// `gc changes --cursor-file`: a reader that advances someone else's cursor
// consumes items before that someone sees them.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/api"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

type listenTarget struct {
	name      string
	isProfile bool // a saved profile (GC_PROFILE may name it for the spawned session)
	apiURL    string
	apiKey    string
}

const maxBackoff = 5 * time.Minute

var intervalRe = regexp.MustCompile(`(?i)^(\d+)\s*(s|m|h)?$`)

// parseInterval reads `60s`, `5m`, `1h`, or bare seconds.
func parseInterval(v string) (time.Duration, error) {
	m := intervalRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, clierr.Usage(fmt.Sprintf("Invalid --interval \"%s\".", v), "Use e.g. 30s, 5m or 1h.")
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour}[strings.ToLower(m[2] + "s")[:1]]
	d := time.Duration(n) * unit
	if n > int64(math.MaxInt64/int64(unit)) {
		d = math.MaxInt64
	}
	if d < 5*time.Second {
		return 0, clierr.Usage("--interval must be at least 5s.")
	}
	return d, nil
}

// collectItems: every array under `data` is a list of change items — new
// kinds count without a code change here.
func collectItems(profile string, data any) []*js.Object {
	items := []*js.Object{}
	o := js.Obj(data)
	if o == nil {
		return items
	}
	for _, kind := range o.Keys() {
		v, _ := o.Get(kind)
		list, ok := js.Arr(v)
		if !ok {
			continue
		}
		for _, x := range list {
			g := func(k ...string) any { return js.Get(x, k...) }
			item := js.O(
				"profile", profile,
				"kind", kind,
				"for", js.Or(g("for"), "self"),
				"id", js.Or(g("id"), g("row_id"), nil),
				"created_at", js.Or(g("created_at"), g("updated_at"), g("delivered_at"), nil),
			)
			for _, k := range []string{"task_id", "doc_id", "table_id"} {
				if v := g(k); js.Truthy(v) {
					item.Set(k, v)
				}
			}
			if t := js.Or(g("title"), g("task_title"), g("table_name"), g("goal", "title")); js.Truthy(t) {
				item.Set("title", t)
			}
			items = append(items, item)
		}
	}
	return items
}

func itemLine(i *js.Object) string {
	g := func(k string) any { v, _ := i.Get(k); return v }
	ref := ""
	switch {
	case js.Truthy(g("task_id")):
		ref = "task " + js.Str(g("task_id"))
	case js.Truthy(g("doc_id")):
		ref = "doc " + js.Str(g("doc_id"))
	case js.Truthy(g("table_id")):
		ref = "datasheet " + js.Str(g("table_id"))
	}
	principal := ""
	if g("for") == "principal" {
		principal = "[principal] "
	}
	title := ""
	if t := g("title"); js.Truthy(t) {
		title = `"` + js.Str(t) + `"`
	}
	parts := []string{js.Str(g("profile")), js.Str(g("kind")), principal + js.Str(js.Or(g("id"), "-")), ref, js.Str(js.Or(g("created_at"), "-")), title}
	kept := []string{}
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "  ")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func cursorPath(env config.Env, name string) string {
	return filepath.Join(config.Dir(env), "listen", unsafeName.ReplaceAllString(name, "_")+".json")
}

func readCursor(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	v, err := js.Parse(b)
	if err != nil {
		return ""
	}
	c, ok := js.Get(v, "cursor").(string)
	if !ok {
		return ""
	}
	if _, ok := js.ParseDate(c); !ok {
		return ""
	}
	return c
}

func writeCursor(path, cursor string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.AtomicWrite(path, js.Stringify(js.O("cursor", cursor))+"\n", 0o600)
}

func resolveTargets(c *Ctx) ([]listenTarget, error) {
	env := c.Sys.Env
	if c.bool("all-profiles") {
		_, hasProfile := c.str("profile")
		_, hasToken := c.str("token")
		if hasProfile || hasToken {
			return nil, clierr.Usage("--all-profiles polls every saved profile; drop --profile/--token.")
		}
		cfg, err := config.Load(env)
		if err != nil {
			return nil, err
		}
		urlFlag, _ := c.str("api-url")
		targets := []listenTarget{}
		for _, name := range cfg.Profiles.Keys() {
			p := cfg.Profile(name)
			key := config.Field(p, "api_key")
			if key == "" {
				continue
			}
			u := urlFlag
			if u == "" {
				u = config.Field(p, "api_url")
			}
			targets = append(targets, listenTarget{name: name, isProfile: true, apiURL: strings.TrimRight(u, "/"), apiKey: key})
		}
		if len(targets) == 0 {
			u := urlFlag
			if u == "" {
				u = env["GC_API_URL"]
			}
			return nil, clierr.NotConnected("no API key", u)
		}
		return targets, nil
	}
	a, err := c.Auth()
	if err != nil {
		return nil, err
	}
	key, err := config.RequireKey(a)
	if err != nil {
		return nil, err
	}
	// A key from --token/GC_API_KEY gets its own cursor, named by a hash (never the key itself).
	isProfile := a.KeySource == "profile" && a.Profile != ""
	name := a.Profile
	if !isProfile {
		sum := sha256.Sum256([]byte(key))
		name = "key-" + hex.EncodeToString(sum[:])[:12]
	}
	return []listenTarget{{name: name, isProfile: isProfile, apiURL: a.APIURL, apiKey: key}}, nil
}

func listenCommand() *Command {
	return &Command{
		Path:    []string{"listen"},
		Summary: "Watch for changes and wake an agent (successor of gc-worker.sh): print items, or run --exec per batch",
		Options: []OptSpec{
			{Name: "interval", Value: "<30s|5m>", Desc: "Time between polls (default 60s, min 5s)"},
			{Name: "once", Bool: true, Desc: "One pass over the profiles, then exit (for cron/launchd)"},
			{Name: "exec", Value: "<shell cmd>", Desc: "Run this via sh -c when there are changes, and wait for it"},
			{Name: "all-profiles", Bool: true, Desc: "Poll every saved profile (one cursor each)"},
			{Name: "since", Value: "<iso|15m|2h>", Desc: "Start here instead of the stored cursor (first run default: now)"},
		},
		Details: strings.Join([]string{
			"One cursor per workspace, owned by the listener: <config dir>/listen/<profile>.json. It advances to the",
			"server's meta.cursor after each successful poll (and, with --exec, once the command has started).",
			"--exec gets: GC_PROFILE, GC_API_KEY, GC_API_URL (bound to that workspace), GC_LISTEN_SINCE (the old cursor:",
			"run `gc changes --since \"$GC_LISTEN_SINCE\"` to see the same items), GC_LISTEN_COUNT, GC_LISTEN_CHANGES_FILE",
			"(the /changes response as JSON). A failing command is logged; the cursor still advances.",
			"Without --exec: one line per item on stdout (JSON lines with --json). Logs go to stderr.",
		}, "\n"),
		Examples: []string{
			`gc listen --once --all-profiles --exec "openclaw cron run gc-worker-spawn"`,
			`gc listen --interval 2m --exec 'claude -p "Run gc changes --since $GC_LISTEN_SINCE and handle it"'`,
			"gc listen --json",
		},
		Run: runListen,
	}
}

type listener struct {
	c        *Ctx
	exec     string
	logMu    sync.Mutex
	mu       sync.Mutex
	stopping bool
	child    *exec.Cmd
	wake     chan struct{}
}

func (l *listener) log(target, msg string) {
	prefix := ""
	if target != "" {
		prefix = "[" + target + "] "
	}
	l.logMu.Lock()
	defer l.logMu.Unlock()
	io.WriteString(l.c.Sys.Stderr, js.NowISO()+" "+prefix+msg+"\n")
}

func (l *listener) isStopping() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopping
}

func (l *listener) onSignal(sig string) {
	l.mu.Lock()
	if l.stopping {
		l.mu.Unlock()
		return
	}
	l.stopping = true
	child := l.child
	l.mu.Unlock()
	suffix := ""
	if child != nil {
		suffix = " after the running command"
	}
	l.log("", sig+" — stopping"+suffix)
	if child != nil && child.Process != nil {
		child.Process.Signal(syscall.SIGTERM)
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func runListen(c *Ctx) error {
	if _, ok := c.str("field"); ok {
		return clierr.Usage("--field does not apply to gc listen; use --json for JSON lines.")
	}
	intervalFlag, ok := c.str("interval")
	if !ok {
		intervalFlag = "60s"
	}
	interval, err := parseInterval(intervalFlag)
	if err != nil {
		return err
	}
	once := c.bool("once")
	execCmd, _ := c.str("exec")
	firstSince := ""
	if s, ok := c.str("since"); ok {
		if firstSince, err = parseSince(s, time.Now()); err != nil {
			return err
		}
	}
	targets, err := resolveTargets(c)
	if err != nil {
		return err
	}

	l := &listener{c: c, exec: execCmd, wake: make(chan struct{}, 1)}
	if c.Sys.OnSignal != nil {
		off := c.Sys.OnSignal(l.onSignal)
		defer off()
	}

	failures := 0
	// --since holds per workspace until that workspace's poll succeeded; a
	// failed first poll must not drop the requested backfill window.
	pending := map[string]bool{}
	if firstSince != "" {
		for _, t := range targets {
			pending[t.name] = true
		}
	}
	for !l.isStopping() {
		results := []string{}
		for _, t := range targets {
			if l.isStopping() {
				break
			}
			override := ""
			if pending[t.name] {
				override = firstSince
			}
			r, err := l.pollOne(t, override)
			if err != nil {
				return err
			}
			if r == "ok" {
				delete(pending, t.name)
			}
			results = append(results, r)
		}
		allRejected := len(results) == len(targets)
		for _, r := range results {
			if r != "unauthorized" {
				allRejected = false
			}
		}
		if allRejected {
			return clierr.NotConnected(clierr.Rejected, targets[0].apiURL)
		}
		if once || l.isStopping() {
			failed := []string{}
			for i, r := range results {
				if r == "error" {
					failed = append(failed, targets[i].name)
				}
			}
			if len(failed) > 0 {
				return clierr.New("Poll failed for: " + strings.Join(failed, ", "))
			}
			return nil
		}
		hadError := false
		for _, r := range results {
			if r == "error" {
				hadError = true
			}
		}
		if hadError {
			failures++
		} else {
			failures = 0
		}
		delay := interval
		if failures > 0 {
			capMs := math.Max(float64(interval.Milliseconds()), float64(maxBackoff.Milliseconds()))
			ms := math.Min(float64(interval.Milliseconds())*math.Pow(2, float64(failures)), capMs)
			delay = time.Duration(ms) * time.Millisecond
			l.log("", fmt.Sprintf("retrying in %ds", int64(math.Floor(ms/1000+0.5))))
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-l.wake:
			timer.Stop()
		}
	}
	return nil
}

// pollOne returns ok | unauthorized | error; a returned error is fatal (e.g. the cursor can't be written).
func (l *listener) pollOne(t listenTarget, sinceOverride string) (string, error) {
	c := l.c
	path := cursorPath(c.Sys.Env, t.name)
	since := sinceOverride
	if since == "" {
		since = readCursor(path)
	}
	if since == "" {
		since = js.NowISO()
	}
	res, err := api.New(t.apiURL, t.apiKey, "").GetChanges(since)
	if err != nil {
		if clierr.IsUnauthorized(err) {
			profileFlag := ""
			if t.isProfile {
				profileFlag = " --profile " + t.name
			}
			l.log(t.name, fmt.Sprintf(`API key rejected (401) — reconnect with: gc onboarding%s --token "gc_live_…"`, profileFlag))
			return "unauthorized", nil
		}
		l.log(t.name, "poll failed: "+err.Error())
		return "error", nil
	}
	next := serverCursor(res)
	if next == "" {
		l.log(t.name, "the server sent no cursor (meta.cursor) — not advancing")
		return "error", nil
	}
	items := collectItems(t.name, js.Get(res, "data"))
	if len(items) == 0 {
		if err := writeCursor(path, next); err != nil {
			return "", err
		}
		l.log(t.name, "0 changes")
		return "ok", nil
	}
	counts := js.NewObject()
	for _, i := range items {
		k := js.Str(js.Get(i, "kind"))
		n, _ := counts.Get(k)
		cur, _ := n.(int)
		counts.Set(k, cur+1)
	}
	parts := []string{}
	for _, k := range counts.Keys() {
		n, _ := counts.Get(k)
		parts = append(parts, fmt.Sprintf("%s %d", k, n))
	}
	byKind := strings.Join(parts, ", ")

	if l.exec == "" {
		for _, i := range items {
			line := itemLine(i)
			if c.bool("json") {
				line = js.Stringify(i)
			}
			if err := c.Out.Line(line); err != nil {
				return "", err
			}
		}
		if err := writeCursor(path, next); err != nil {
			return "", err
		}
		l.log(t.name, fmt.Sprintf("%d changes (%s)", len(items), byKind))
		return "ok", nil
	}

	tmp, err := os.MkdirTemp("", "gc-listen-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	changesFile := filepath.Join(tmp, "changes.json")
	if err := os.WriteFile(changesFile, []byte(js.StringifyIndent(res)), 0o600); err != nil {
		return "", err
	}
	childEnv := map[string]string{}
	for k, v := range c.Sys.Env {
		childEnv[k] = v
	}
	childEnv["GC_API_KEY"] = t.apiKey
	childEnv["GC_API_URL"] = t.apiURL
	childEnv["GC_LISTEN_SINCE"] = since
	childEnv["GC_LISTEN_COUNT"] = strconv.Itoa(len(items))
	childEnv["GC_LISTEN_CHANGES_FILE"] = changesFile
	// Only name a profile that exists — a GC_PROFILE the session's gc can't find is an error there.
	if t.isProfile {
		childEnv["GC_PROFILE"] = t.name
	} else {
		delete(childEnv, "GC_PROFILE")
	}
	envList := make([]string, 0, len(childEnv))
	for k, v := range childEnv {
		envList = append(envList, k+"="+v)
	}
	cmd := exec.Command("sh", "-c", l.exec)
	cmd.Env = envList
	cmd.Stdout = c.Sys.Stdout
	cmd.Stderr = c.Sys.Stderr
	if err := cmd.Start(); err != nil {
		l.log(t.name, "exec could not start: "+err.Error()+" — cursor not advanced")
		return "error", nil
	}
	l.mu.Lock()
	l.child = cmd
	if l.stopping { // the signal arrived while the command was starting
		cmd.Process.Signal(syscall.SIGTERM)
	}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.child = nil
		l.mu.Unlock()
	}()
	// Started: these items are handed over. Advance now, so a listener killed
	// while the session runs doesn't hand them over twice.
	if err := writeCursor(path, next); err != nil {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		return "", err
	}
	l.log(t.name, fmt.Sprintf("%d changes (%s) → exec (pid %d)", len(items), byKind, cmd.Process.Pid))
	code := 0
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			l.log(t.name, "exec failed: "+err.Error())
			return "ok", nil
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128
		} else {
			code = ee.ExitCode()
		}
	}
	if code == 0 {
		l.log(t.name, "exec finished (exit 0)")
	} else {
		l.log(t.name, fmt.Sprintf("exec exited with %d — cursor advanced anyway, these changes are not retried", code))
	}
	return "ok", nil
}
