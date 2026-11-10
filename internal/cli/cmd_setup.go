package cli

// `gc setup openclaw` — wires gc into an OpenClaw host, replacing what the
// OpenClaw plugin `groundcontrol` did at gateway boot: the worker spawn
// cron(s), the three nightly summary crons, the skill, and the listener that
// wakes the worker (launchd on macOS, the container entrypoint in a fleet
// container). Idempotent: every item is compared with what is there, and only
// what is missing or outdated is written.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

// Seams for the tests: the platform and the path of the running gc binary.
var (
	setupGOOS       = runtime.GOOS
	setupExecutable = os.Executable
	setupUID        = os.Getuid
)

const (
	spawnCronName  = "gc-worker-spawn"
	listenLabel    = "ai.groundcontrol.listen"
	spawnSchedule  = "0 0 1 1 *" // placeholder: the job is disabled and only fired by `openclaw cron run`
	listenInterval = 300
	// The markers an up-to-date spawn message carries. A spawn cron whose
	// message lacks one (or still names the plugin's groundcontrol_* tools)
	// predates gc and gets its message replaced.
	schedulingMarker = "ZEITVERSETZTE AUFTRÄGE"
	closingMarker    = "GC_OK"
	changesMarker    = "changes --cursor-file"
	pluginToolMarker = "groundcontrol_"
)

// ─── OpenClaw access ──────────────────────────────────────────────────────────

// lookPath finds an executable on the PATH of the run's environment (not the
// process's: tests and launchd hand gc a PATH of their own).
func lookPath(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// findOpenclaw: GC_OPENCLAW_BIN when set (and nothing else — an explicit
// override that doesn't exist means "no openclaw"), else `openclaw` on PATH.
func findOpenclaw(env config.Env) string {
	if b := env["GC_OPENCLAW_BIN"]; b != "" {
		if !strings.Contains(b, "/") {
			return lookPath(b, env["PATH"])
		}
		if st, err := os.Stat(b); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			if abs, err := filepath.Abs(b); err == nil {
				return abs
			}
			return b
		}
		return ""
	}
	return lookPath("openclaw", env["PATH"])
}

func envList(env config.Env) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

type openclaw struct {
	bin string
	env config.Env
}

// run calls the openclaw CLI and returns its stdout.
func (o openclaw) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Env = envList(o.env)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		last := ""
		for _, l := range strings.Split(strings.TrimSpace(errb.String()), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "ExperimentalWarning") && !strings.Contains(l, "--trace-warnings") {
				last = l
			}
		}
		if last != "" {
			return "", fmt.Errorf("openclaw %s: %v (%s)", args[0]+" "+args[1], err, last)
		}
		return "", fmt.Errorf("openclaw %s: %v", args[0]+" "+args[1], err)
	}
	return out.String(), nil
}

type cronJob struct {
	id, name, message, model    string
	enabled                     bool
	deliveryMode, channel, dest string
}

// listCrons reads `openclaw cron list --all --json` ({jobs:[{id,name,enabled,
// payload:{model,message},delivery:{mode,channel,to}}]}).
func (o openclaw) listCrons() ([]cronJob, error) {
	out, err := o.run("cron", "list", "--all", "--json")
	if err != nil {
		return nil, err
	}
	// Tolerate a banner or warning line before the JSON.
	if i := strings.IndexByte(out, '{'); i > 0 {
		out = out[i:]
	}
	v, err := js.Parse([]byte(out))
	if err != nil {
		return nil, fmt.Errorf("openclaw cron list --json did not return JSON: %s", err)
	}
	jobs := []cronJob{}
	for _, j := range js.List(js.Get(v, "jobs")) {
		s := func(k ...string) string {
			x := js.Get(j, k...)
			if js.Nullish(x) {
				return ""
			}
			return js.Str(x)
		}
		jobs = append(jobs, cronJob{
			id: s("id"), name: s("name"), message: s("payload", "message"), model: s("payload", "model"),
			enabled:      js.Get(j, "enabled") != false,
			deliveryMode: s("delivery", "mode"), channel: s("delivery", "channel"), dest: s("delivery", "to"),
		})
	}
	return jobs, nil
}

func findCron(jobs []cronJob, name string) *cronJob {
	for i := range jobs {
		if jobs[i].name == name && jobs[i].id != "" {
			return &jobs[i]
		}
	}
	return nil
}

// ─── ~/.openclaw/openclaw.json ────────────────────────────────────────────────

func openclawHome(env config.Env) string { return filepath.Join(config.Home(env), ".openclaw") }

func readOpenclawConfig(env config.Env) any {
	b, err := os.ReadFile(filepath.Join(openclawHome(env), "openclaw.json"))
	if err != nil {
		return nil
	}
	v, err := js.Parse(b)
	if err != nil {
		return nil
	}
	return v
}

// pluginActive: the OpenClaw plugin `groundcontrol` still runs on this host.
// It counts as active unless its entry says enabled:false, or plugins.allow is
// a list without it — and it has to be mentioned somewhere (an entry, or in
// plugins.allow). The plugin and gc would otherwise both create crons and
// both wake the agent.
func pluginActive(cfg any) bool {
	plugins := js.Get(cfg, "plugins")
	entry := js.Get(plugins, "entries", "groundcontrol")
	if js.Get(entry, "enabled") == false {
		return false
	}
	allowed := false
	allow, isList := js.Arr(js.Get(plugins, "allow"))
	for _, a := range allow {
		if a == "groundcontrol" {
			allowed = true
		}
	}
	if isList && !allowed {
		return false
	}
	return js.Obj(entry) != nil || allowed
}

// telegramChat is the user's Telegram chat: channels.telegram.allowFrom[0], or
// on a multi-account install the default account's allowFrom[0].
func telegramChat(cfg any) string {
	tg := js.Get(cfg, "channels", "telegram")
	first := func(v any) string {
		if l := js.List(v); len(l) > 0 && js.Truthy(l[0]) {
			return js.Str(l[0])
		}
		return ""
	}
	if c := first(js.Get(tg, "allowFrom")); c != "" {
		return c
	}
	accounts := js.Obj(js.Get(tg, "accounts"))
	if accounts == nil || accounts.Len() == 0 {
		return ""
	}
	name := js.Str(js.Or(js.Get(tg, "defaultAccount"), "default"))
	account, ok := accounts.Get(name)
	if !ok {
		account, _ = accounts.Get(accounts.Keys()[0])
	}
	return first(js.Get(account, "allowFrom"))
}

// ownerAgent is the agent the gc crons belong to, or "" when the host has a
// single agent (openclaw picks it). With several agents `openclaw cron add`
// refuses to guess ("Multiple agents are configured, but this operation has no
// explicit owner"), so gc names one: GC_OPENCLAW_AGENT, else "main" when it
// exists, else the agent marked default, else the first one. Agents live under
// agents.entries (a map, OpenClaw 10.x) or agents.list (an array, 9.x).
func ownerAgent(cfg any, env config.Env) string {
	if a := env["GC_OPENCLAW_AGENT"]; a != "" {
		return a
	}
	agents := js.Get(cfg, "agents")
	var ids []string
	def := ""
	if m := js.Obj(js.Get(agents, "entries")); m != nil {
		for _, k := range m.Keys() {
			ids = append(ids, k)
			if v, _ := m.Get(k); js.Get(v, "default") == true && def == "" {
				def = k
			}
		}
	}
	for _, a := range js.List(js.Get(agents, "list")) {
		id := js.Str(js.Get(a, "id"))
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if js.Get(a, "default") == true && def == "" {
			def = id
		}
	}
	if len(ids) < 2 {
		return ""
	}
	for _, id := range ids {
		if id == "main" {
			return id
		}
	}
	if def != "" {
		return def
	}
	return ids[0]
}

// ─── the prompts ──────────────────────────────────────────────────────────────

// cronClosing ends every gc cron prompt. Load-bearing: an empty final message
// makes OpenClaw record a successful run as failed ("Agent couldn't generate a
// response").
const cronClosing = `Schliesse den Turn IMMER mit einer kurzen finalen Textzeile ab (z. B. "GC_OK" oder ` +
	`"GC_OK - 2 Comments beantwortet"). Diese Zeile geht NICHT an deinen User (dieser Cron ` +
	`liefert nichts aus), sie beendet nur den Lauf sauber - ohne sie wertet die Runtime den ` +
	`Lauf als Fehler ("Agent couldn't generate a response").`

func schedulingSection(gc string) string {
	return schedulingMarker + ` / ERINNERUNGEN aus GROUNDCONTROL (z.B. „erinnere mich morgen um 9“, ` +
		`„kommentiere morgen früh hier …“): Das automations-/cron-Tool ist in diesem Lauf gesperrt ` +
		`(„restricted to the current automation“) — lege den Auftrag stattdessen mit dem exec-Tool über die ` +
		`Kommandozeile an. Zeit als lokale Zeit deines Users (z.B. Europe/Vienna), Agent-ID meist main. ` +
		"Erinnerung an deinen User: `openclaw cron add --agent <agentId> --name \"<kurzer Name>\" --at \"<YYYY-MM-DDTHH:MM:SS>\" " +
		"--tz <Zeitzone> --session current --session-key agent:<agentId>:main --announce --channel <Kanal, z.B. telegram> " +
		"--to <Chat-ID deines Users> --delete-after-run --message \"Deine Antwort ist genau diese Nachricht an <User>: <Text>\"`. " +
		"Spätere Aktion in GROUNDCONTROL (z.B. ein Kommentar): `openclaw cron add --agent <agentId> --name \"<kurzer Name>\" " +
		"--at \"<YYYY-MM-DDTHH:MM:SS>\" --tz <Zeitzone> --session isolated --no-deliver --delete-after-run " +
		"--message \"<vollständiger Auftrag inkl. Task-ID und dem gc-Aufruf (" + gc + " …)>\"`. Prüfe danach mit `openclaw cron list --all --json`, " +
		`dass der Job existiert. Bestätige in GROUNDCONTROL NUR mit der Job-ID, wenn der Befehl erfolgreich war. ` +
		`Klappt es nicht, sag das offen (mit der Fehlermeldung) — sage nie etwas zu, das nicht eingerichtet ist.`
}

// gcInvocation is how a prompt calls gc: `gc --profile <p>`, or `gc` without a profile.
func gcInvocation(profile string) string {
	if profile == "" {
		return "gc"
	}
	return "gc --profile " + profile
}

func gcPathNote(gcPath string) string {
	if gcPath == "" {
		return ""
	}
	return " (Findet dein exec-Tool `gc` nicht, nimm den vollen Pfad " + gcPath + ".)"
}

func spawnMessage(profile, gcPath string) string {
	gc := gcInvocation(profile)
	head := "GROUNDCONTROL hat neue Aktivität."
	if profile != "" {
		head = "GROUNDCONTROL hat neue Aktivität im Workspace „" + profile + "“. Nutze für JEDEN gc-Aufruf in diesem Lauf `" + gc +
			"` — nie ohne --profile, nie ein anderes Profil."
	}
	return head + gcPathNote(gcPath) + "\n\n" +
		"1. Hol die neuen Changes mit `" + gc + " changes --cursor-file memory/gc-changes.json` (der Cursor wird dabei " +
		"fortgeschrieben, du speicherst nichts selbst) und deine offenen Tasks mit `" + gc + " tasks list --assigned-to me --status todo,in_progress`.\n" +
		"2. Erledige in GROUNDCONTROL, was zu tun ist: Comments beantworten (`" + gc + " comment <task-id> --body-file -`), " +
		"Tasks bearbeiten und abschließen (`" + gc + " tasks update <task-id> --status …` — im Scrum-Workflow review, sonst done; " +
		"`" + gc + " context` nennt den Workflow). Brauchst du eine Entscheidung eines Menschen, frag mit `" + gc +
		" tasks ask <task-id> --body-file -` statt mit einem Frage-Kommentar. Einträge mit [principal] betreffen deinen User: " +
		"nicht selbst starten, ihm Bescheid geben. Goal-Check-ins aus den Changes reichst du mit `" + gc + " goals checkin-submit` ein.\n" +
		"3. Die laufende Arbeit bleibt in GROUNDCONTROL. NUR wenn du eine echte Benachrichtigung oder Rückfrage an deinen User hast, " +
		"die nicht in GROUNDCONTROL bleiben kann, sende sie über dein Telegram-/message-Tool an den User. Wenn es nichts Akutes zu melden " +
		"gibt, sende NICHTS — keine Routine-, Status- oder „nichts zu tun“-Nachrichten. Keine Zusammenfassungen ohne Anlass.\n" +
		"Details zur Arbeitsweise: `" + gc + " guide openclaw`.\n\n" +
		schedulingSection(gc) + "\n\n" +
		cronClosing
}

// spawnNeedsRepair: the message predates gc (or this gc's prompt) and is replaced.
func spawnNeedsRepair(message, profile string) bool {
	if strings.Contains(message, pluginToolMarker) {
		return true
	}
	for _, m := range []string{schedulingMarker, closingMarker, changesMarker} {
		if !strings.Contains(message, m) {
			return true
		}
	}
	return profile != "" && !strings.Contains(message, gcInvocation(profile)+" ")
}

// Every summary cron works through all saved profiles.
const perProfile = "– für jedes gc-Profil einzeln: Liste die Profile mit `gc profiles --json` und setze bei jedem Aufruf " +
	"`gc --profile <profil>` (zeigt die Liste keine Profile, arbeite ohne --profile)."

func journalMessage(gcPath string) string {
	return "Du bist der GROUNDCONTROL-Agent. Erstelle den täglichen Journal-Eintrag " + perProfile + gcPathNote(gcPath) + "\n" +
		"1. Hol den heutigen Tag (lokales Datum, z.B. per `date +%F`): `gc --profile <profil> journal get <YYYY-MM-DD>`.\n" +
		"2. Schreibe eine Zusammenfassung in 2–4 Sätzen und speichere sie: `gc --profile <profil> journal summary <YYYY-MM-DD> --body-file -` (Text über stdin).\n" +
		"Fokus: Was wurde erledigt, was hat sich bewegt, was ist offen. Keine Rückfragen, keine Nachricht an deinen User.\n\n" +
		cronClosing
}

func initiativeSummariesMessage(gcPath string) string {
	return "Du bist der GROUNDCONTROL-Agent. Aktualisiere die Initiative-Summaries " + perProfile + gcPathNote(gcPath) + "\n" +
		"1. Liste die Initiativen: `gc --profile <profil> initiatives list`. Überspringe archivierte und solche mit „(contents not visible to you)“.\n" +
		"2. Für jede Initiative: Details mit `gc --profile <profil> initiatives get <id>` und Memory mit `gc --profile <profil> initiatives memory <id>`.\n" +
		"3. Verdichte den aktuellen Stand auf 2–3 Sätze und speichere ihn: `gc --profile <profil> initiatives update <id> --summary-file -` (Text über stdin).\n" +
		"Fokus: aktueller Stand, letzte Fortschritte, nächste Schritte. Keine Rückfragen, keine Nachricht an deinen User.\n\n" +
		cronClosing
}

func memorySummariesMessage(gcPath string) string {
	return "Du bist der GROUNDCONTROL-Agent. Aktualisiere die Memory-Summaries " + perProfile + gcPathNote(gcPath) + "\n" +
		"1. Liste die Initiativen: `gc --profile <profil> initiatives list`. Überspringe archivierte und solche mit „(contents not visible to you)“.\n" +
		"2. Für jede: Memory mit `gc --profile <profil> initiatives memory <id>`.\n" +
		"3. Wenn recent_insights oder key_decisions vorhanden sind: verdichte sie zu einer memory_summary (3–5 Sätze) und speichere sie: " +
		"`gc --profile <profil> initiatives update <id> --memory-summary-file -` (Text über stdin; den Zeitstempel setzt der Server). " +
		"Nur Initiativen aktualisieren, die wirklich neue Erkenntnisse haben.\n" +
		"Keine Rückfragen, keine Nachricht an deinen User.\n\n" +
		cronClosing
}

type summaryCron struct {
	name, schedule string
	message        func(gcPath string) string
}

var summaryCrons = []summaryCron{
	{"gc-journal", "0 23 * * *", journalMessage},
	{"gc-initiative-summaries", "15 23 * * *", initiativeSummariesMessage},
	{"gc-memory-summaries", "30 23 * * *", memorySummariesMessage},
}

// ─── the plan ─────────────────────────────────────────────────────────────────

// setupAction is one item of the plan. status is what will happen (create,
// update, unchanged, skip, manual); apply does it.
type setupAction struct {
	item, status, detail string
	apply                func() error
}

var doneForm = map[string]string{"create": "created", "update": "updated", "load": "loaded", "skip": "skipped"}

type setupReport struct {
	dryRun  bool
	actions []*setupAction
	failed  int
	guarded bool
}

func (r *setupReport) result() any {
	rows := []any{}
	for _, a := range r.actions {
		rows = append(rows, js.O("item", a.item, "status", a.status, "detail", a.detail))
	}
	return js.O("dry_run", r.dryRun, "actions", rows)
}

func (r *setupReport) lines() []string {
	ls := []string{}
	for _, a := range r.actions {
		st := a.status
		if r.dryRun && doneForm[st] != "" {
			st = "would " + st
		}
		line := output.PadEnd(st, 9) + " " + a.item
		if a.detail != "" {
			line += " — " + a.detail
		}
		ls = append(ls, line)
	}
	return ls
}

func keyedProfiles(env config.Env) ([]string, error) {
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, n := range cfg.Profiles.Keys() {
		if config.Field(cfg.Profile(n), "api_key") != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@,+-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// writeIfChanged plans a file write: create | update | unchanged.
func writeIfChanged(item, path, content string, mode, dirMode os.FileMode, after func(changed bool) error) *setupAction {
	status := "create"
	if cur, err := os.ReadFile(path); err == nil {
		status = "update"
		if string(cur) == content {
			status = "unchanged"
		}
	}
	return &setupAction{item: item, status: status, detail: path, apply: func() error {
		if status != "unchanged" {
			if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
				return err
			}
			if err := config.AtomicWrite(path, content, mode); err != nil {
				return err
			}
			if err := os.Chmod(path, mode); err != nil {
				return err
			}
		}
		if after != nil {
			return after(status != "unchanged")
		}
		return nil
	}}
}

func launchctlBin(env config.Env) string {
	if b := env["GC_LAUNCHCTL_BIN"]; b != "" {
		return b
	}
	return "launchctl"
}

func runLaunchctl(env config.Env, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, launchctlBin(env), args...)
	cmd.Env = envList(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %v %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func listenPlist(gcPath string, args []string, env map[string]string, logPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + listenLabel + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range append([]string{gcPath}, args...) {
		b.WriteString("    <string>" + xmlText(a) + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>StartInterval</key>
  <integer>` + strconv.Itoa(listenInterval) + `</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>EnvironmentVariables</key>
  <dict>
`)
	for _, k := range []string{"PATH", "GC_CONFIG_DIR", "GC_OPENCLAW_BIN"} {
		if v, ok := env[k]; ok && v != "" {
			b.WriteString("    <key>" + k + "</key>\n    <string>" + xmlText(v) + "</string>\n")
		}
	}
	b.WriteString(`  </dict>
  <key>StandardOutPath</key>
  <string>` + xmlText(logPath) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlText(logPath) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// launchdPath is the PATH the listener runs with: launchd's own is
// /usr/bin:/bin, where neither gc nor openclaw (nor node) lives.
func launchdPath(dirs ...string) string {
	seen := map[string]bool{}
	out := []string{}
	for _, d := range append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin") {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, ":")
}

func listenArgs(wakeCmd string) []string {
	return []string{"listen", "--once", "--all-profiles", "--kinds", strings.Join(OpenclawWorkerKinds, ","), "--exec", wakeCmd}
}

// planSetup builds the action list. Nothing is changed here.
func planSetup(c *Ctx, oc openclaw, ocConfig any) ([]*setupAction, error) {
	env := c.Sys.Env
	gcPath, err := setupExecutable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate the gc binary: %w", err)
	}
	if abs, err := filepath.Abs(gcPath); err == nil {
		gcPath = abs
	}
	profiles, err := keyedProfiles(env)
	if err != nil {
		return nil, err
	}
	jobs, err := oc.listCrons()
	if err != nil {
		return nil, clierr.New("Cannot list the OpenClaw crons: "+err.Error(), "Is the OpenClaw gateway running? Nothing was changed.")
	}
	actions := []*setupAction{}

	// Spawn cron(s): one for ≤1 profile, else one per profile.
	type spawn struct{ name, profile string }
	spawns := []spawn{}
	if len(profiles) <= 1 {
		p := ""
		if len(profiles) == 1 {
			p = profiles[0]
		}
		spawns = append(spawns, spawn{spawnCronName, p})
	} else {
		for _, p := range profiles {
			spawns = append(spawns, spawn{spawnCronName + "-" + p, p})
		}
	}
	// The operator-tuned legacy spawn cron: model + delivery are inherited.
	legacy := findCron(jobs, spawnCronName)
	if legacy == nil {
		for i := range jobs {
			if strings.HasPrefix(jobs[i].name, spawnCronName+"-") && jobs[i].id != "" {
				legacy = &jobs[i]
				break
			}
		}
	}
	chat := telegramChat(ocConfig)
	agent := ownerAgent(ocConfig, oc.env)
	ours := map[string]bool{}
	for _, s := range spawns {
		s := s
		ours[s.name] = true
		item := "cron " + s.name
		msg := spawnMessage(s.profile, gcPath)
		if j := findCron(jobs, s.name); j != nil {
			if !spawnNeedsRepair(j.message, s.profile) {
				actions = append(actions, &setupAction{item: item, status: "unchanged"})
				continue
			}
			id := j.id
			actions = append(actions, &setupAction{item: item, status: "update", detail: "message replaced (gc prompt)", apply: func() error {
				_, err := oc.run("cron", "edit", id, "--message", msg)
				return err
			}})
			continue
		}
		args := []string{"cron", "add", "--name", s.name, "--cron", spawnSchedule, "--session", "isolated", "--message", msg, "--light-context", "--disabled"}
		notes := []string{"disabled, fired by the listener"}
		if agent != "" {
			args = append(args, "--agent", agent)
			notes = append(notes, "agent "+agent)
		}
		if legacy != nil && legacy.model != "" {
			args = append(args, "--model", legacy.model)
			notes = append(notes, "model "+legacy.model+" from "+legacy.name)
		}
		switch {
		case legacy != nil && legacy.channel != "" && legacy.channel != "last" && legacy.dest != "":
			args = append(args, "--channel", legacy.channel, "--to", legacy.dest)
			notes = append(notes, legacy.channel+" binding from "+legacy.name)
		case chat != "":
			args = append(args, "--channel", "telegram", "--to", chat)
			notes = append(notes, "telegram binding")
		}
		if legacy != nil && legacy.deliveryMode == "announce" {
			args = append(args, "--announce")
			notes = append(notes, "announce (as "+legacy.name+")")
		} else {
			args = append(args, "--no-deliver")
		}
		actions = append(actions, &setupAction{item: item, status: "create", detail: strings.Join(notes, ", "), apply: func() error {
			_, err := oc.run(args...)
			return err
		}})
	}
	for _, j := range jobs {
		if (j.name == spawnCronName || strings.HasPrefix(j.name, spawnCronName+"-")) && !ours[j.name] {
			actions = append(actions, &setupAction{item: "cron " + j.name, status: "manual",
				detail: "not used by gc (no such profile) — remove it with: openclaw cron rm " + j.id})
		}
	}

	// The nightly summary crons.
	for _, sc := range summaryCrons {
		item := "cron " + sc.name
		msg := sc.message(gcPath)
		if j := findCron(jobs, sc.name); j != nil {
			if !strings.Contains(j.message, pluginToolMarker) {
				detail := ""
				if !j.enabled {
					detail = "disabled — enable with: openclaw cron edit " + j.id + " --enable"
				}
				actions = append(actions, &setupAction{item: item, status: "unchanged", detail: detail})
				continue
			}
			id := j.id
			// --no-deliver too: the plugin created these without a delivery
			// flag, i.e. announce/last, and the gc prompt ends on a GC_OK line
			// — kept as announce, the user would get "GC_OK" every night.
			actions = append(actions, &setupAction{item: item, status: "update", detail: "message replaced (gc prompt), delivery off, enabled state kept", apply: func() error {
				_, err := oc.run("cron", "edit", id, "--message", msg, "--no-deliver")
				return err
			}})
			continue
		}
		args := []string{"cron", "add", "--name", sc.name, "--cron", sc.schedule, "--session", "isolated", "--message", msg, "--light-context", "--no-deliver"}
		if agent != "" {
			args = append(args, "--agent", agent)
		}
		detail := "enabled"
		if len(profiles) == 0 {
			args = append(args, "--disabled")
			detail = "disabled (no gc profile yet) — enable with openclaw cron edit <id> --enable"
		}
		actions = append(actions, &setupAction{item: item, status: "create", detail: detail, apply: func() error {
			_, err := oc.run(args...)
			return err
		}})
	}

	// The skill, the OpenClaw variant; setup owns it, so a differing copy is overwritten.
	skillTgt := skillTarget{"openclaw", "openclaw", filepath.Join(openclawHome(env), "skills")}
	planned, err := installSkills(skillTgt, true, true)
	if err != nil {
		return nil, err
	}
	for _, r := range planned {
		status := map[string]string{"installed": "create", "updated": "update"}[js.Str(js.Get(r, "status"))]
		if status == "" {
			status = "unchanged"
		}
		name := js.Str(js.Get(r, "skill"))
		path := js.Str(js.Get(r, "path"))
		actions = append(actions, &setupAction{item: "skill " + name, status: status, detail: path, apply: func() error {
			if status == "unchanged" {
				return nil
			}
			for _, s := range Skills() {
				if s.Name == name {
					if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
						return err
					}
					return os.WriteFile(path, []byte(s.ContentFor("openclaw")), 0o666)
				}
			}
			return nil
		}})
	}

	// The listener.
	switch {
	case env["GC_OPENCLAW_LISTENER"] == "entrypoint":
		marker := filepath.Join(config.Dir(env), "listen.enabled")
		a := writeIfChanged("listener marker", marker, "# Written by gc setup openclaw: the container entrypoint runs gc listen while this file exists.\n", 0o600, 0o700, nil)
		if a.status != "unchanged" {
			a.detail += " (the container entrypoint starts gc listen)"
		}
		actions = append(actions, a)
	case setupGOOS == "darwin":
		wakePath := filepath.Join(openclawHome(env), "bin", "gc-listen-wake")
		wake := "#!/bin/sh\n# Written by gc setup openclaw: wakes the OpenClaw worker for the workspace in $GC_PROFILE.\nexec " + shellQuote(gcPath) + " setup openclaw-wake\n"
		actions = append(actions, writeIfChanged("wake script", wakePath, wake, 0o755, 0o755, nil))

		logPath := filepath.Join(openclawHome(env), "logs", "gc-listen.log")
		plistPath := filepath.Join(config.Home(env), "Library", "LaunchAgents", listenLabel+".plist")
		plistEnv := map[string]string{"PATH": launchdPath(filepath.Dir(oc.bin), filepath.Dir(gcPath))}
		if env["GC_CONFIG_DIR"] != "" {
			plistEnv["GC_CONFIG_DIR"] = env["GC_CONFIG_DIR"]
		}
		if env["GC_OPENCLAW_BIN"] != "" {
			plistEnv["GC_OPENCLAW_BIN"] = oc.bin
		}
		plist := listenPlist(gcPath, listenArgs(shellQuote(wakePath)), plistEnv, logPath)
		domain := "gui/" + strconv.Itoa(setupUID())
		target := domain + "/" + listenLabel
		// Unchanged and loaded: nothing to do. Unchanged but not loaded: load it.
		// Changed: a loaded job only picks up a new plist after a reload.
		loaded := false
		plistAction := writeIfChanged("launchd "+listenLabel, plistPath, plist, 0o644, 0o755, func(changed bool) error {
			if loaded && !changed {
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
				return err
			}
			if changed {
				runLaunchctl(env, "bootout", target) // not loaded yet is fine
			}
			return runLaunchctl(env, "bootstrap", domain, plistPath)
		})
		if plistAction.status == "unchanged" {
			if runLaunchctl(env, "print", target) == nil {
				loaded = true
			} else {
				plistAction.status = "load"
				plistAction.detail += " (was not loaded)"
			}
		}
		actions = append(actions, plistAction)
		legacyWorker := filepath.Join(config.Home(env), "Library", "LaunchAgents", "ai.groundcontrol.worker.plist")
		if fileExists(legacyWorker) {
			actions = append(actions, &setupAction{item: "launchd ai.groundcontrol.worker", status: "manual",
				detail: "the plugin's gc-worker.sh is still installed; remove it with: launchctl bootout " + domain + "/ai.groundcontrol.worker && rm " + legacyWorker})
		}
	default:
		line := fmt.Sprintf("*/5 * * * * %s %s >> %s 2>&1", shellQuote(gcPath), strings.Join(quoteAll(listenArgs(shellQuote(gcPath)+" setup openclaw-wake")), " "),
			filepath.Join(openclawHome(env), "logs", "gc-listen.log"))
		actions = append(actions, &setupAction{item: "listener", status: "manual",
			detail: "no launchd here — run gc listen from cron or systemd, e.g. this crontab line:\n  " + line})
	}
	return actions, nil
}

func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shellQuote(a)
	}
	return out
}

// setupOpenclaw runs the whole setup and returns the report. A plugin that
// is still active stops it before anything is touched (report.guarded).
func setupOpenclaw(c *Ctx, dryRun bool) (*setupReport, error) {
	env := c.Sys.Env
	r := &setupReport{dryRun: dryRun}
	ocConfig := readOpenclawConfig(env)
	if pluginActive(ocConfig) {
		r.guarded = true
		r.actions = append(r.actions, &setupAction{item: "openclaw plugin groundcontrol", status: "skipped",
			detail: "still active — gc set up nothing. Disable the plugin first (plugins.entries.groundcontrol.enabled: false in ~/.openclaw/openclaw.json, restart the gateway), then run gc setup openclaw again."})
		return r, nil
	}
	bin := findOpenclaw(env)
	if bin == "" {
		return nil, clierr.New("openclaw not found (PATH, or GC_OPENCLAW_BIN).", "Install OpenClaw first, or point GC_OPENCLAW_BIN at the binary.")
	}
	actions, err := planSetup(c, openclaw{bin: bin, env: env}, ocConfig)
	if err != nil {
		return nil, err
	}
	r.actions = actions
	if dryRun {
		return r, nil
	}
	for _, a := range actions {
		if a.apply == nil {
			if d := doneForm[a.status]; d != "" {
				a.status = d
			}
			continue
		}
		if err := a.apply(); err != nil {
			a.status = "failed"
			a.detail = strings.TrimSpace(a.detail + " " + err.Error())
			r.failed++
			continue
		}
		if d := doneForm[a.status]; d != "" {
			a.status = d
		}
	}
	return r, nil
}

func setupCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"setup", "openclaw"},
			Summary: "Wire gc into this OpenClaw host: worker crons, summary crons, skill, listener (idempotent)",
			Options: []OptSpec{
				{Name: "dry-run", Bool: true, Desc: "Print what would change, change nothing"},
			},
			Details: strings.Join([]string{
				"Replaces the OpenClaw plugin groundcontrol. Safe to rerun: only what is missing or outdated changes.",
				"",
				"Guard: while the plugin is active (an entry under plugins.entries.groundcontrol without enabled:false,",
				"or groundcontrol in plugins.allow — unless plugins.allow is a list without it), nothing is set up and",
				"gc exits 0 with a hint: both would wake the agent. Needs openclaw on PATH or GC_OPENCLAW_BIN (else exit 1).",
				"",
				"Agent: with several agents on the host (agents.entries / agents.list) the crons are added with --agent:",
				"GC_OPENCLAW_AGENT, else main, else the default agent, else the first. A single-agent host gets no --agent.",
				"",
				"Crons (openclaw cron add; existing names are never duplicated):",
				"  gc-worker-spawn                 ≤1 gc profile; with several: gc-worker-spawn-<profile> per profile.",
				"                                  Disabled, fired by the listener. --session isolated --light-context, no",
				"                                  delivery; Telegram bound from openclaw.json allowFrom. Model and delivery",
				"                                  are inherited from an existing gc-worker-spawn(-*) cron. An existing spawn",
				"                                  cron whose message predates gc gets the gc prompt (openclaw cron edit).",
				"  gc-journal (23:00), gc-initiative-summaries (23:15), gc-memory-summaries (23:30)",
				"                                  Created enabled when gc has a profile, else disabled. An existing one keeps",
				"                                  its enabled state; its message is replaced only if it still uses the plugin's",
				"                                  groundcontrol_* tools.",
				"Skill: the OpenClaw variant of groundcontrol into ~/.openclaw/skills (overwritten when it differs).",
				"Listener:",
				"  macOS      ~/Library/LaunchAgents/" + listenLabel + ".plist (every 5 min: gc listen --once --all-profiles",
				"             --kinds <openclaw worker set> --exec ~/.openclaw/bin/gc-listen-wake), loaded with launchctl.",
				"  container  GC_OPENCLAW_LISTENER=entrypoint: only <config dir>/listen.enabled; the entrypoint runs gc listen.",
				"  Linux      prints a crontab line to add yourself.",
			}, "\n"),
			Examples: []string{"gc setup openclaw --dry-run", "gc setup openclaw", "gc setup openclaw --json"},
			Run: func(c *Ctx) error {
				r, err := setupOpenclaw(c, c.bool("dry-run"))
				if err != nil {
					return err
				}
				if err := c.Out.Emit(r.result(), r.lines); err != nil {
					return err
				}
				if r.failed > 0 {
					return clierr.New(fmt.Sprintf("%d setup step(s) failed — see above. Fix the cause and run gc setup openclaw again.", r.failed))
				}
				return nil
			},
		},
		{
			Path:    []string{"setup", "openclaw-wake"},
			Summary: "Wake the OpenClaw worker for $GC_PROFILE (what the listener's --exec runs)",
			Details: strings.Join([]string{
				"Finds the cron gc-worker-spawn-<GC_PROFILE>, else gc-worker-spawn, in openclaw cron list --all --json",
				"and runs it (openclaw cron run <id>). Logs one line to stderr. Exit 1 when there is no such cron.",
			}, "\n"),
			Examples: []string{`GC_PROFILE=acme gc setup openclaw-wake`, `gc listen --once --all-profiles --exec "gc setup openclaw-wake"`},
			Run:      runOpenclawWake,
		},
	}
}

func runOpenclawWake(c *Ctx) error {
	env := c.Sys.Env
	bin := findOpenclaw(env)
	if bin == "" {
		return clierr.New("openclaw not found (PATH, or GC_OPENCLAW_BIN).")
	}
	oc := openclaw{bin: bin, env: env}
	jobs, err := oc.listCrons()
	if err != nil {
		return clierr.New("Cannot list the OpenClaw crons: " + err.Error())
	}
	profile := env["GC_PROFILE"]
	names := []string{spawnCronName}
	if profile != "" {
		names = []string{spawnCronName + "-" + profile, spawnCronName}
	}
	for _, n := range names {
		if j := findCron(jobs, n); j != nil {
			if _, err := oc.run("cron", "run", j.id); err != nil {
				return clierr.New(err.Error())
			}
			who := profile
			if who == "" {
				who = "the current profile"
			}
			io.WriteString(c.Sys.Stderr, fmt.Sprintf("%s woke %s (%s) for %s\n", js.NowISO(), n, j.id, who))
			return nil
		}
	}
	return clierr.New(fmt.Sprintf("No spawn cron %s found.", strings.Join(names, " or ")), "Run: gc setup openclaw")
}
