package cli

// `gc setup openclaw` / `gc setup openclaw-wake` / onboarding → setup, all
// hermetic: openclaw and launchctl are this test binary behind symlinks (see
// TestMain), HOME and the config dir are temp dirs, and the platform and the
// gc path are the package seams. Nothing here can reach a real gateway.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
)

// TestMain doubles as the fake openclaw / launchctl when invoked through a
// symlink of that name.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "openclaw":
		os.Exit(fakeOpenclaw(os.Args[1:]))
	case "launchctl":
		os.Exit(fakeLaunchctl(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeJob struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Enabled  bool           `json:"enabled"`
	Payload  map[string]any `json:"payload"`
	Delivery map[string]any `json:"delivery,omitempty"`
}

type fakeState struct {
	Jobs []fakeJob `json:"jobs"`
}

func logCall(env string, args []string) {
	f, err := os.OpenFile(os.Getenv(env), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(args)
	f.Write(append(b, '\n'))
}

// fakeOpenclaw keeps its crons in $FAKE_OC_STATE and logs every call to $FAKE_OC_LOG.
func fakeOpenclaw(args []string) int {
	logCall("FAKE_OC_LOG", args)
	statePath := os.Getenv("FAKE_OC_STATE")
	var st fakeState
	if b, err := os.ReadFile(statePath); err == nil {
		json.Unmarshal(b, &st)
	}
	if len(args) < 2 || args[0] != "cron" {
		return 2
	}
	if os.Getenv("FAKE_OC_FAIL") == args[1] {
		fmt.Fprintln(os.Stderr, "boom: gateway unreachable")
		return 1
	}
	flags := map[string]string{}
	bools := map[string]bool{}
	pos := []string{}
	for i := 2; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		switch a {
		case "--all", "--json", "--disabled", "--light-context", "--no-deliver", "--announce", "--enable":
			bools[a] = true
		default:
			if i+1 < len(args) {
				flags[a] = args[i+1]
				i++
			}
		}
	}
	save := func() {
		b, _ := json.Marshal(st)
		os.WriteFile(statePath, b, 0o600)
	}
	switch args[1] {
	case "list":
		fmt.Fprintln(os.Stderr, "(node:1) ExperimentalWarning: SQLite is an experimental feature")
		b, _ := json.Marshal(st)
		os.Stdout.Write(b)
	case "add":
		j := fakeJob{ID: fmt.Sprintf("job-%d", len(st.Jobs)+1), Name: flags["--name"], Enabled: !bools["--disabled"],
			Payload: map[string]any{"message": flags["--message"]}}
		if flags["--model"] != "" {
			j.Payload["model"] = flags["--model"]
		}
		mode := "announce"
		if bools["--no-deliver"] {
			mode = "none"
		}
		j.Delivery = map[string]any{"mode": mode, "channel": flags["--channel"], "to": flags["--to"]}
		st.Jobs = append(st.Jobs, j)
		save()
	case "edit":
		for i := range st.Jobs {
			if st.Jobs[i].ID == pos[0] {
				if m, ok := flags["--message"]; ok {
					st.Jobs[i].Payload["message"] = m
				}
				if bools["--no-deliver"] {
					if st.Jobs[i].Delivery == nil {
						st.Jobs[i].Delivery = map[string]any{}
					}
					st.Jobs[i].Delivery["mode"] = "none"
				}
			}
		}
		save()
	case "run":
	default:
		return 2
	}
	return 0
}

// fakeLaunchctl logs to $FAKE_LC_LOG; `print` fails unless $FAKE_LC_LOADED=1.
func fakeLaunchctl(args []string) int {
	logCall("FAKE_LC_LOG", args)
	if len(args) > 0 && args[0] == "print" && os.Getenv("FAKE_LC_LOADED") != "1" {
		return 113
	}
	return 0
}

type setupHost struct {
	t                   *testing.T
	home, cfgDir, bin   string
	state, ocLog, lcLog string
	env                 config.Env
	stdin               string
	stdinTTY            bool
}

const fakeGc = "/opt/gc/bin/gc"

func newSetupHost(t *testing.T, goos string, profiles ...string) *setupHost {
	t.Helper()
	tmp := t.TempDir()
	h := &setupHost{t: t, home: filepath.Join(tmp, "home"), cfgDir: filepath.Join(tmp, "config"), bin: filepath.Join(tmp, "bin")}
	h.state, h.ocLog, h.lcLog = filepath.Join(tmp, "oc.json"), filepath.Join(tmp, "oc.log"), filepath.Join(tmp, "lc.log")
	for _, d := range []string{h.home, h.cfgDir, h.bin, filepath.Join(h.home, ".openclaw")} {
		os.MkdirAll(d, 0o700)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"openclaw", "launchctl"} {
		if err := os.Symlink(self, filepath.Join(h.bin, n)); err != nil {
			t.Fatal(err)
		}
	}
	h.env = config.Env{
		"HOME": h.home, "GC_CONFIG_DIR": h.cfgDir, "PATH": h.bin,
		"GC_LAUNCHCTL_BIN": filepath.Join(h.bin, "launchctl"),
		"FAKE_OC_STATE":    h.state, "FAKE_OC_LOG": h.ocLog, "FAKE_LC_LOG": h.lcLog,
	}
	if len(profiles) > 0 {
		ps := []string{}
		for i, p := range profiles {
			ps = append(ps, fmt.Sprintf(`%q:{"api_url":"http://x/api/v1","api_key":"gc_live_profile%04d","workspace":%q,"agent":"Robo","created_at":"2026-10-01T00:00:00Z"}`, p, i, p))
		}
		os.WriteFile(filepath.Join(h.cfgDir, "config.json"), []byte(fmt.Sprintf(`{"current":%q,"profiles":{%s}}`, profiles[0], strings.Join(ps, ","))), 0o600)
	}
	oldGOOS, oldExe, oldUID := setupGOOS, setupExecutable, setupUID
	setupGOOS = goos
	setupExecutable = func() (string, error) { return fakeGc, nil }
	setupUID = func() int { return 501 }
	t.Cleanup(func() { setupGOOS, setupExecutable, setupUID = oldGOOS, oldExe, oldUID })
	return h
}

func (h *setupHost) jobs(js ...fakeJob) {
	b, _ := json.Marshal(fakeState{Jobs: js})
	os.WriteFile(h.state, b, 0o600)
}

func (h *setupHost) openclawJSON(s string) {
	os.WriteFile(filepath.Join(h.home, ".openclaw", "openclaw.json"), []byte(s), 0o600)
}

func (h *setupHost) run(args ...string) (int, string, string) {
	h.t.Helper()
	c := capture(h.env)
	c.sys.Stdin = strings.NewReader(h.stdin)
	c.sys.StdinIsTTY = h.stdinTTY
	code := Run(args, c.sys)
	return code, c.out.String(), c.err.String()
}

func readCalls(path string) [][]string {
	b, _ := os.ReadFile(path)
	out := [][]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var a []string
		json.Unmarshal([]byte(l), &a)
		out = append(out, a)
	}
	return out
}

// mutating calls only (no list)
func (h *setupHost) writes() [][]string {
	out := [][]string{}
	for _, c := range readCalls(h.ocLog) {
		if len(c) > 1 && c[1] != "list" {
			out = append(out, c)
		}
	}
	return out
}

func (h *setupHost) state_() fakeState {
	var st fakeState
	b, _ := os.ReadFile(h.state)
	json.Unmarshal(b, &st)
	return st
}

func (h *setupHost) job(name string) *fakeJob {
	st := h.state_()
	for i := range st.Jobs {
		if st.Jobs[i].Name == name {
			return &st.Jobs[i]
		}
	}
	return nil
}

func addCall(calls [][]string, name string) []string {
	for _, c := range calls {
		if len(c) > 3 && c[1] == "add" && c[3] == name {
			return c
		}
	}
	return nil
}

func flagVal(call []string, flag string) string {
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}

func hasFlag(call []string, flag string) bool {
	for _, a := range call {
		if a == flag {
			return true
		}
	}
	return false
}

func TestSetupOpenclawFreshMacThenRerun(t *testing.T) {
	h := newSetupHost(t, "darwin", "acme")
	h.openclawJSON(`{"plugins":{"entries":{"groundcontrol":{"enabled":false}}},"channels":{"telegram":{"defaultAccount":"main","accounts":{"main":{"allowFrom":["4242"]}}}}}`)
	code, out, errOut := h.run("setup", "openclaw")
	if code != 0 {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
	calls := h.writes()
	spawn := addCall(calls, "gc-worker-spawn")
	if spawn == nil {
		t.Fatalf("no spawn cron: %v", calls)
	}
	if flagVal(spawn, "--cron") != "0 0 1 1 *" || !hasFlag(spawn, "--disabled") || flagVal(spawn, "--session") != "isolated" ||
		!hasFlag(spawn, "--light-context") || !hasFlag(spawn, "--no-deliver") || hasFlag(spawn, "--announce") ||
		flagVal(spawn, "--channel") != "telegram" || flagVal(spawn, "--to") != "4242" || hasFlag(spawn, "--model") {
		t.Fatalf("spawn args %q", spawn)
	}
	msg := flagVal(spawn, "--message")
	for _, want := range []string{"gc --profile acme changes --cursor-file memory/gc-changes.json", "gc --profile acme tasks list --assigned-to me --status todo,in_progress",
		"ZEITVERSETZTE AUFTRÄGE", "openclaw cron add", "gc --profile acme guide openclaw", fakeGc} {
		if !strings.Contains(msg, want) {
			t.Fatalf("spawn message lacks %q:\n%s", want, msg)
		}
	}
	if !strings.HasSuffix(msg, `("Agent couldn't generate a response").`) {
		t.Fatal("spawn message must end with the GC_OK closing")
	}
	for _, sc := range []struct{ name, cron string }{{"gc-journal", "0 23 * * *"}, {"gc-initiative-summaries", "15 23 * * *"}, {"gc-memory-summaries", "30 23 * * *"}} {
		c := addCall(calls, sc.name)
		if c == nil || flagVal(c, "--cron") != sc.cron || hasFlag(c, "--disabled") || !hasFlag(c, "--light-context") || !hasFlag(c, "--no-deliver") || flagVal(c, "--session") != "isolated" {
			t.Fatalf("%s: %q", sc.name, c)
		}
	}
	skill, _ := os.ReadFile(filepath.Join(h.home, ".openclaw", "skills", "groundcontrol", "SKILL.md"))
	if string(skill) != Skills()[0].Variants["openclaw"] {
		t.Fatal("openclaw skill variant not installed")
	}
	wakePath := filepath.Join(h.home, ".openclaw", "bin", "gc-listen-wake")
	wake, _ := os.ReadFile(wakePath)
	if !strings.Contains(string(wake), "exec /opt/gc/bin/gc setup openclaw-wake\n") || fileMode(t, wakePath) != 0o755 {
		t.Fatalf("wake script %q", wake)
	}
	plistPath := filepath.Join(h.home, "Library", "LaunchAgents", "ai.groundcontrol.listen.plist")
	plist, _ := os.ReadFile(plistPath)
	for _, want := range []string{"<string>ai.groundcontrol.listen</string>", "<string>/opt/gc/bin/gc</string>\n    <string>listen</string>\n    <string>--once</string>\n    <string>--all-profiles</string>\n    <string>--kinds</string>\n    <string>task_comments,doc_comments,tasks_created,tasks_updated,docs_updated,goal_checkins,table_rows_created,table_rows_updated,table_comments</string>\n    <string>--exec</string>\n    <string>" + wakePath + "</string>",
		"<integer>300</integer>", "<key>RunAtLoad</key>\n  <true/>", "<string>" + h.bin + ":/opt/gc/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<key>GC_CONFIG_DIR</key>", filepath.Join(h.home, ".openclaw", "logs", "gc-listen.log")} {
		if !strings.Contains(string(plist), want) {
			t.Fatalf("plist lacks %q:\n%s", want, plist)
		}
	}
	lc := readCalls(h.lcLog)
	if len(lc) != 2 || lc[0][0] != "bootout" || lc[0][1] != "gui/501/ai.groundcontrol.listen" || strings.Join(lc[1], " ") != "bootstrap gui/501 "+plistPath {
		t.Fatalf("launchctl calls %v", lc)
	}
	for _, want := range []string{"created   cron gc-worker-spawn", "created   cron gc-journal", "created   skill groundcontrol", "created   wake script", "created   launchd ai.groundcontrol.listen"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}

	// Rerun: nothing changes, the loaded job is left alone.
	os.Remove(h.ocLog)
	os.Remove(h.lcLog)
	h.env["FAKE_LC_LOADED"] = "1"
	code, out, _ = h.run("setup", "openclaw", "--json")
	if code != 0 {
		t.Fatal(out)
	}
	if w := h.writes(); len(w) != 0 {
		t.Fatalf("rerun wrote %v", w)
	}
	if lc := readCalls(h.lcLog); len(lc) != 1 || lc[0][0] != "print" {
		t.Fatalf("rerun launchctl %v", lc)
	}
	var res struct {
		DryRun  bool `json:"dry_run"`
		Actions []struct{ Item, Status string }
	}
	json.Unmarshal([]byte(out), &res)
	if len(res.Actions) < 8 {
		t.Fatalf("actions %s", out)
	}
	for _, a := range res.Actions {
		if a.Status != "unchanged" {
			t.Fatalf("rerun: %s is %s", a.Item, a.Status)
		}
	}

	// Unchanged plist but not loaded → bootstrap only.
	os.Remove(h.lcLog)
	h.env["FAKE_LC_LOADED"] = "0"
	code, out, _ = h.run("setup", "openclaw")
	if lc := readCalls(h.lcLog); code != 0 || len(lc) != 2 || lc[1][0] != "bootstrap" || !strings.Contains(out, "loaded    launchd") {
		t.Fatalf("%d %v\n%s", code, lc, out)
	}
}

func TestSetupOpenclawInheritsFromLegacySpawnPerProfile(t *testing.T) {
	h := newSetupHost(t, "linux", "alpha", "beta")
	h.openclawJSON(`{"channels":{"telegram":{"allowFrom":["777"]}}}`)
	h.jobs(
		fakeJob{ID: "old1", Name: "gc-worker-spawn", Payload: map[string]any{"model": "anthropic/claude-sonnet", "message": "Hol die Changes … GC_OK"},
			Delivery: map[string]any{"mode": "announce", "channel": "telegram", "to": "999"}},
		fakeJob{ID: "j2", Name: "gc-journal", Enabled: true, Payload: map[string]any{"message": "Nutze groundcontrol_get_journal_day …"}},
		fakeJob{ID: "j3", Name: "gc-initiative-summaries", Enabled: false, Payload: map[string]any{"message": "gc initiatives list …"}},
	)
	code, out, errOut := h.run("setup", "openclaw")
	if code != 0 {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
	calls := h.writes()
	for _, p := range []string{"alpha", "beta"} {
		c := addCall(calls, "gc-worker-spawn-"+p)
		if c == nil || flagVal(c, "--model") != "anthropic/claude-sonnet" || flagVal(c, "--channel") != "telegram" || flagVal(c, "--to") != "999" ||
			!hasFlag(c, "--announce") || hasFlag(c, "--no-deliver") || !hasFlag(c, "--disabled") {
			t.Fatalf("%s: %q", p, c)
		}
		msg := flagVal(c, "--message")
		if !strings.Contains(msg, "gc --profile "+p+" changes --cursor-file") || strings.Contains(msg, "--profile "+map[string]string{"alpha": "beta", "beta": "alpha"}[p]) {
			t.Fatalf("%s message names the wrong profile", p)
		}
	}
	if addCall(calls, "gc-worker-spawn") != nil {
		t.Fatal("multi-profile must not create the plain spawn cron")
	}
	// The plugin-era journal gets the gc prompt (enabled state untouched); the
	// gc-era initiative summaries stay as they are, disabled included.
	j := h.job("gc-journal")
	if !strings.Contains(j.Payload["message"].(string), "gc --profile <profil> journal summary") || !j.Enabled {
		t.Fatalf("journal %v", j)
	}
	// …and stops announcing: the gc prompt ends on GC_OK, which must not reach the user nightly.
	if j.Delivery == nil || j.Delivery["mode"] != "none" {
		t.Fatalf("repaired journal still delivers: %v", j.Delivery)
	}
	if h.job("gc-initiative-summaries").Payload["message"] != "gc initiatives list …" {
		t.Fatal("a gc-era summary cron was rewritten")
	}
	if addCall(calls, "gc-memory-summaries") == nil {
		t.Fatal("missing memory summaries cron not created")
	}
	for _, want := range []string{"manual    cron gc-worker-spawn — not used by gc", "openclaw cron rm old1", "updated   cron gc-journal",
		"unchanged cron gc-initiative-summaries — disabled — enable with: openclaw cron edit j3 --enable", "manual    listener", "*/5 * * * * /opt/gc/bin/gc listen --once"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(h.home, "Library")); err == nil {
		t.Fatal("linux must not write a LaunchAgent")
	}
	if len(readCalls(h.lcLog)) != 0 {
		t.Fatal("linux must not call launchctl")
	}
	// Rerun: nothing more to do.
	os.Remove(h.ocLog)
	if code, out, _ := h.run("setup", "openclaw"); code != 0 || len(h.writes()) != 0 {
		t.Fatalf("rerun wrote %v\n%s", h.writes(), out)
	}
}

func TestSetupOpenclawRepairsAPluginSpawnCron(t *testing.T) {
	h := newSetupHost(t, "linux", "acme")
	plugin := "GROUNDCONTROL hat neue Aktivität. Hol die Changes (Cursor: memory/heartbeat-state.json) … ZEITVERSETZTE AUFTRÄGE … GC_OK"
	h.jobs(fakeJob{ID: "s1", Name: "gc-worker-spawn", Payload: map[string]any{"model": "x/y", "message": plugin}})
	if code, out, _ := h.run("setup", "openclaw"); code != 0 || !strings.Contains(out, "updated   cron gc-worker-spawn") {
		t.Fatal(out)
	}
	calls := h.writes()
	if len(calls) == 0 || calls[0][1] != "edit" || calls[0][2] != "s1" || calls[0][3] != "--message" {
		t.Fatalf("calls %v", calls)
	}
	if !strings.Contains(h.job("gc-worker-spawn").Payload["message"].(string), "gc --profile acme changes --cursor-file") {
		t.Fatal("message not replaced")
	}
	// A matching message is left alone.
	os.Remove(h.ocLog)
	if code, _, _ := h.run("setup", "openclaw"); code != 0 || len(h.writes()) != 0 {
		t.Fatalf("rerun %v", h.writes())
	}
	if !spawnNeedsRepair(spawnMessage("acme", "")+" groundcontrol_get_task", "acme") || spawnNeedsRepair(spawnMessage("", ""), "") || !spawnNeedsRepair(spawnMessage("", ""), "acme") {
		t.Fatal("spawnNeedsRepair")
	}
}

func TestSetupOpenclawGuardMissingBinaryAndNoProfiles(t *testing.T) {
	for _, cfg := range []string{
		`{"plugins":{"entries":{"groundcontrol":{"enabled":true,"config":{}}}}}`,
		`{"plugins":{"allow":["telegram","groundcontrol"]}}`,
		`{"plugins":{"entries":{"groundcontrol":{"config":{}}}}}`,
	} {
		h := newSetupHost(t, "darwin", "acme")
		h.openclawJSON(cfg)
		code, out, _ := h.run("setup", "openclaw")
		if code != 0 || !strings.Contains(out, "skipped   openclaw plugin groundcontrol — still active") {
			t.Fatalf("%s: %d %s", cfg, code, out)
		}
		if len(readCalls(h.ocLog)) != 0 {
			t.Fatalf("%s: the guard must not even list crons", cfg)
		}
		if _, err := os.Stat(filepath.Join(h.home, ".openclaw", "skills")); err == nil {
			t.Fatal("the guard must not install the skill")
		}
	}
	for _, cfg := range []string{`{"plugins":{"entries":{"groundcontrol":{"enabled":false}}}}`, `{"plugins":{"allow":["telegram"],"entries":{"groundcontrol":{"config":{}}}}}`, `{}`} {
		if pluginActive(parse(t, cfg)) {
			t.Fatalf("%s counted as active", cfg)
		}
	}

	h := newSetupHost(t, "darwin")
	h.env["PATH"] = t.TempDir()
	if code, _, errOut := h.run("setup", "openclaw"); code != 1 || !strings.Contains(errOut, "openclaw not found") {
		t.Fatalf("%d %s", code, errOut)
	}
	h.env["GC_OPENCLAW_BIN"] = filepath.Join(h.bin, "openclaw")
	h.env["FAKE_OC_FAIL"] = "list"
	if code, _, errOut := h.run("setup", "openclaw"); code != 1 || !strings.Contains(errOut, "Cannot list the OpenClaw crons") || !strings.Contains(errOut, "boom: gateway unreachable") {
		t.Fatalf("%d %s", code, errOut)
	}
	delete(h.env, "FAKE_OC_FAIL")
	// No profile yet: plain gc in the spawn prompt, summary crons created disabled.
	h.env["GC_OPENCLAW_LISTENER"] = "entrypoint"
	if code, out, errOut := h.run("setup", "openclaw"); code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	calls := h.writes()
	if c := addCall(calls, "gc-journal"); !hasFlag(c, "--disabled") {
		t.Fatalf("journal %q", c)
	}
	if msg := flagVal(addCall(calls, "gc-worker-spawn"), "--message"); strings.Contains(msg, "--profile") || !strings.Contains(msg, "`gc changes --cursor-file") {
		t.Fatalf("spawn message %s", msg)
	}
	// A failing add is reported per item and exits 1; the rest still runs.
	h2 := newSetupHost(t, "linux", "acme")
	h2.env["FAKE_OC_FAIL"] = "add"
	code, out, errOut := h2.run("setup", "openclaw")
	if code != 1 || !strings.Contains(out, "failed    cron gc-journal") || !strings.Contains(errOut, "setup step(s) failed") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(h2.home, ".openclaw", "skills", "groundcontrol", "SKILL.md")); err != nil {
		t.Fatal("the skill is still installed after a failing cron")
	}
}

func TestSetupOpenclawContainerAndDryRun(t *testing.T) {
	h := newSetupHost(t, "darwin", "acme")
	h.env["GC_OPENCLAW_LISTENER"] = "entrypoint"
	code, out, errOut := h.run("setup", "openclaw", "--dry-run")
	if code != 0 {
		t.Fatalf("%s %s", out, errOut)
	}
	for _, want := range []string{"would create cron gc-worker-spawn", "would create skill groundcontrol", "would create listener marker"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run lacks %q:\n%s", want, out)
		}
	}
	if w := h.writes(); len(w) != 0 {
		t.Fatalf("dry run wrote crons %v", w)
	}
	for _, p := range []string{filepath.Join(h.home, ".openclaw", "skills"), filepath.Join(h.cfgDir, "listen.enabled")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("dry run wrote %s", p)
		}
	}
	code, out, _ = h.run("setup", "openclaw", "--dry-run", "--json")
	if code != 0 || !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"status": "create"`) {
		t.Fatal(out)
	}

	if code, out, _ := h.run("setup", "openclaw"); code != 0 || !strings.Contains(out, "created   listener marker") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(h.cfgDir, "listen.enabled")); err != nil {
		t.Fatal("no marker")
	}
	if _, err := os.Stat(filepath.Join(h.home, "Library")); err == nil {
		t.Fatal("container mode must not write a LaunchAgent")
	}
	if _, err := os.Stat(filepath.Join(h.home, ".openclaw", "bin")); err == nil {
		t.Fatal("container mode must not write the wake script")
	}
	if len(readCalls(h.lcLog)) != 0 {
		t.Fatal("container mode must not call launchctl")
	}
}

func TestOpenclawWake(t *testing.T) {
	h := newSetupHost(t, "linux")
	h.jobs(fakeJob{ID: "plain", Name: "gc-worker-spawn"}, fakeJob{ID: "b1", Name: "gc-worker-spawn-beta"})
	run := func(profile string) (int, string, []string) {
		os.Remove(h.ocLog)
		h.env["GC_PROFILE"] = profile
		code, _, errOut := h.run("setup", "openclaw-wake")
		runs := []string{}
		for _, c := range readCalls(h.ocLog) {
			if c[1] == "run" {
				runs = append(runs, c[2])
			}
		}
		return code, errOut, runs
	}
	if code, errOut, runs := run("beta"); code != 0 || strings.Join(runs, ",") != "b1" || !strings.Contains(errOut, "woke gc-worker-spawn-beta (b1) for beta") {
		t.Fatalf("%d %s %v", code, errOut, runs)
	}
	if code, _, runs := run("alpha"); code != 0 || strings.Join(runs, ",") != "plain" {
		t.Fatalf("%d %v", code, runs)
	}
	h.jobs(fakeJob{ID: "b1", Name: "gc-worker-spawn-beta"})
	if code, errOut, runs := run("alpha"); code != 1 || len(runs) != 0 || !strings.Contains(errOut, "No spawn cron gc-worker-spawn-alpha or gc-worker-spawn found") {
		t.Fatalf("%d %s %v", code, errOut, runs)
	}
}

// The cron prompts tell agents to run gc commands — they must exist, like
// every snippet in the skills and the README.
func TestCronPromptsNameRealCommands(t *testing.T) {
	msgs := []string{spawnMessage("acme", "/x/gc"), spawnMessage("", "")}
	for _, sc := range summaryCrons {
		msgs = append(msgs, sc.message("/x/gc"))
	}
	n := 0
	for _, m := range msgs {
		for _, s := range snippets(m) {
			n++
			if msg := checkSnippet(s); msg != "" {
				t.Errorf("%q %s", s, msg)
			}
		}
		if !strings.HasSuffix(m, cronClosing) {
			t.Error("every cron prompt ends with the GC_OK closing")
		}
		if strings.Contains(m, pluginToolMarker) {
			t.Error("a gc prompt names a plugin tool")
		}
	}
	if n < 15 {
		t.Fatalf("only %d snippets found", n)
	}
}

func TestOnboardingRunsOpenclawSetup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"user":{"display_name":"Robo","is_agent":true},"tenant":{"name":"Acme","workflow":"kanban"}}}`))
	}))
	defer srv.Close()
	key := "gc_live_onboardSetup0001"

	// No TTY: setup runs on its own after the onboarding.
	h := newSetupHost(t, "linux")
	h.stdin = key
	code, out, errOut := h.run("onboarding", "--token", "-", "--api-url", srv.URL+"/api/v1")
	if code != 0 || !strings.Contains(out, "OpenClaw setup:") || addCall(h.writes(), "gc-worker-spawn") == nil {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if msg := flagVal(addCall(h.writes(), "gc-worker-spawn"), "--message"); !strings.Contains(msg, "gc --profile acme changes") {
		t.Fatal("setup ran before the profile was saved")
	}

	// --json keeps stdout one JSON document; the setup report goes to stderr.
	h = newSetupHost(t, "linux")
	h.stdin = key
	code, out, errOut = h.run("onboarding", "--token", "-", "--api-url", srv.URL+"/api/v1", "--json")
	var v map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || !strings.Contains(errOut, "OpenClaw setup:") {
		t.Fatalf("%d %q %s", code, out, errOut)
	}

	// --no-openclaw skips it.
	h = newSetupHost(t, "linux")
	h.stdin = key
	if code, out, _ := h.run("onboarding", "--token", "-", "--api-url", srv.URL+"/api/v1", "--no-openclaw"); code != 0 || strings.Contains(out, "OpenClaw") || len(readCalls(h.ocLog)) != 0 {
		t.Fatal(out)
	}

	// A failing setup warns; the onboarding still exits 0.
	h = newSetupHost(t, "linux")
	h.stdin = key
	h.env["FAKE_OC_FAIL"] = "list"
	if code, _, errOut := h.run("onboarding", "--token", "-", "--api-url", srv.URL+"/api/v1"); code != 0 || !strings.Contains(errOut, "warning: gc setup openclaw failed") {
		t.Fatalf("%d %s", code, errOut)
	}

	// A TTY asks; "n" (or nothing) skips, "y" runs it.
	for answer, ran := range map[string]bool{"n\n": false, "\n": false, "y\n": true} {
		h = newSetupHost(t, "linux")
		h.stdinTTY = true
		h.stdin = answer
		code, _, errOut := h.run("onboarding", "--token", key, "--api-url", srv.URL+"/api/v1")
		if code != 0 || !strings.Contains(errOut, "[y/N]") || (addCall(h.writes(), "gc-worker-spawn") != nil) != ran {
			t.Fatalf("%q: %d %s", answer, code, errOut)
		}
	}

	// No openclaw on this host: nothing is asked or run.
	h = newSetupHost(t, "linux")
	h.env["PATH"] = t.TempDir()
	h.stdinTTY = true
	if code, _, errOut := h.run("onboarding", "--token", key, "--api-url", srv.URL+"/api/v1"); code != 0 || strings.Contains(errOut, "[y/N]") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}
