package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

type captured struct {
	sys      *Sys
	out, err *bytes.Buffer
}

func capture(env config.Env) captured {
	var out, errb bytes.Buffer
	return captured{&Sys{Env: env, Stdout: &out, Stderr: &errb, Version: "0.0.0-test"}, &out, &errb}
}

func parse(t *testing.T, s string) any {
	t.Helper()
	v, err := js.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"15m": "2026-10-02T11:45:00.000Z", "2h": "2026-10-02T10:00:00.000Z", "1d": "2026-10-01T12:00:00.000Z",
		"90s": "2026-10-02T11:58:30.000Z", "1w": "2026-09-25T12:00:00.000Z", "2H": "2026-10-02T10:00:00.000Z",
		"2026-10-02T08:00:00Z": "2026-10-02T08:00:00.000Z", "2026-10-02T10:00:00+02:00": "2026-10-02T08:00:00.000Z",
		"2026-10-02": "2026-10-02T00:00:00.000Z",
	}
	for in, want := range cases {
		if got, err := parseSince(in, now); err != nil || got != want {
			t.Fatalf("%s → %s %v", in, got, err)
		}
	}
	for _, bad := range []string{"5", "yesterday", "15x", "", "2026-13-45"} {
		if _, err := parseSince(bad, now); err == nil || !strings.Contains(err.Error(), "Invalid --since") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestChangesFormatting(t *testing.T) {
	res := parse(t, `{"data":{"task_comments":[{"task_id":"t","task_title":"T","content":"hello","created_at":"2026-10-02T08:00:00Z","for":"principal"}],"tasks_created":[],"goal_checkins":[{"id":"ci1","kind":"weekly","goal":{"id":"g1","title":"Grow"},"signals":[{"code":"stalled"},"behind_pace"]}]}}`)
	text := strings.Join(formatChanges(res, "2026-10-02T07:00:00.000Z", ""), "\n")
	for _, want := range []string{`[principal] task t "T" · 2026-10-02 08:00Z`, "do NOT start it",
		`weekly check-in ci1 for goal "Grow" (g1) · signals: stalled, behind_pace`,
		"read: gc changes --since 2026-10-02T07:00:00.000Z --field data.goal_checkins.0",
		"submit: gc goals checkin-submit g1 ci1 --body-file -"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if !strings.Contains(strings.Join(formatChanges(parse(t, `{"data":{}}`), "2026-10-02T07:00:00.000Z", ""), "\n"), "Nothing new.") {
		t.Fatal("empty")
	}
	if serverCursor(parse(t, `{"meta":{"cursor":"2026-10-02T12:00:00.000Z","checked_at":"2026-10-02T12:00:05.000Z"}}`)) != "2026-10-02T12:00:00.000Z" ||
		serverCursor(parse(t, `{"meta":{"checked_at":"2026-10-02T12:00:05.000Z"}}`)) != "2026-10-02T12:00:05.000Z" ||
		serverCursor(parse(t, `{"meta":{"cursor":"soon"}}`)) != "" {
		t.Fatal("serverCursor")
	}
}

func TestDatasheetsAndGoalsHelpers(t *testing.T) {
	fields := js.List(parse(t, `[{"id":"fld_a","name":"Company","type":"text"},{"id":"fld_b","name":"Stage","type":"single_select","options":[{"id":"opt_1","label":"Won"}]},{"id":"fld_m","name":"Tags","type":"multi_select","options":[{"id":"opt_2","label":"Hot"}]}]`))
	row := parse(t, `{"id":"r1","data":{"fld_a":"Acme","fld_b":"opt_1","fld_x":null,"fld_m":["opt_2","opt_9"],"fld_n":3}}`)
	if l := rowLine(row, fields); l != "r1  Company=Acme · Stage=Won · Tags=Hot, opt_9 · fld_n=3" {
		t.Fatal(l)
	}
	kr, err := parseKr("Subscribers|5000|subs")
	if err != nil || js.Stringify(kr) != `{"title":"Subscribers","target_value":5000,"unit":"subs"}` {
		t.Fatal(js.Stringify(kr), err)
	}
	if _, err := parseKr("X|lots"); err == nil {
		t.Fatal("non-numeric target")
	}
	f, err := parseFieldSpec("Stage:single_select:New,Won:required")
	if err != nil || js.Stringify(f) != `{"name":"Stage","type":"single_select","options":["New","Won"],"required":true}` {
		t.Fatal(js.Stringify(f), err)
	}
	if _, err := parseFieldSpec("Stage"); err == nil {
		t.Fatal("missing type")
	}
}

func TestListenHelpers(t *testing.T) {
	cases := map[string]time.Duration{"60s": time.Minute, "5m": 5 * time.Minute, "30": 30 * time.Second, "1h": time.Hour}
	for in, want := range cases {
		if d, err := parseInterval(in); err != nil || d != want {
			t.Fatalf("%s → %v %v", in, d, err)
		}
	}
	if _, err := parseInterval("1s"); err == nil || !strings.Contains(err.Error(), "at least 5s") {
		t.Fatal(err)
	}
	if _, err := parseInterval("soon"); err == nil || !strings.Contains(err.Error(), "Invalid --interval") {
		t.Fatal(err)
	}
	items := collectItems("p", parse(t, `{"a":[{"id":1}],"b":[],"c":"not a list","d":[{"id":2,"for":"principal"},{"row_id":3}]}`))
	got := []string{}
	for _, i := range items {
		got = append(got, js.Str(js.Get(i, "kind"))+":"+js.Str(js.Get(i, "id"))+":"+js.Str(js.Get(i, "for")))
	}
	if strings.Join(got, " ") != "a:1:self d:2:principal d:3:self" {
		t.Fatal(got)
	}
}

func TestArgumentParsing(t *testing.T) {
	path := func(args ...string) string {
		r, err := resolveCommand(args)
		if err != nil {
			return "error: " + err.Error()
		}
		if r.command == nil {
			return "group " + r.group
		}
		return strings.Join(r.command.Path, " ")
	}
	cases := map[string][]string{
		"tasks list":   {"tasks", "list", "--status", "todo"},
		"tasks get":    {"--profile", "x", "tasks", "get", "id1"},
		"context":      {"--json", "context"},
		"profiles":     {"profiles"},
		"profiles use": {"profiles", "use", "x"},
		"group tasks":  {"tasks"},
		"comment":      {"comment", "id", "body"},
	}
	for want, args := range cases {
		if got := path(args...); got != want {
			t.Fatalf("%v → %s, want %s", args, got, want)
		}
	}
	if got := path("tasks", "frobnicate"); got != `error: Unknown command "gc tasks frobnicate".` {
		t.Fatal(got)
	}
	if got := path("frobnicate"); !strings.HasPrefix(got, "error: Unknown command") {
		t.Fatal(got)
	}
	get := FindCommand("tasks", "get")
	if _, _, err := parseCommandArgs(get, []string{"id", "--nope"}); err == nil || err.Error() != "Unknown option '--nope'." {
		t.Fatal(err)
	}
	if _, _, err := parseCommandArgs(get, []string{}); err == nil || !strings.Contains(err.Error(), "Missing argument") {
		t.Fatal(err)
	}
	if _, _, err := parseCommandArgs(get, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), `Unexpected argument "b"`) {
		t.Fatal(err)
	}
	v, pos, err := parseCommandArgs(FindCommand("comment"), []string{"--json", "id", "--body-file=f", "--", "--x"})
	if err != nil || v["json"] != true || v["body-file"] != "f" || strings.Join(pos, ",") != "id,--x" {
		t.Fatal(v, pos, err)
	}
	if _, _, err := parseCommandArgs(FindCommand("changes"), []string{"--since", "-5m"}); err == nil || !strings.Contains(err.Error(), "argument is ambiguous") {
		t.Fatal(err)
	}
	if v, _, _ := parseCommandArgs(FindCommand("comment"), []string{"id", "--body-file", "-"}); v["body-file"] != "-" {
		t.Fatal("a lone - is a value")
	}
	if v, _, _ := parseCommandArgs(FindCommand("goals", "create"), []string{"--kr", "a", "--kr", "b"}); strings.Join(v["kr"].([]string), ",") != "a,b" {
		t.Fatal("repeatable")
	}
	// A command option shadows the global of the same name: here --field is a column, not --field <path>.
	v, _, err = parseCommandArgs(FindCommand("tables", "create"), []string{"--field", "A:text", "--field", "B:text"})
	if err != nil || strings.Join(v["field"].([]string), ",") != "A:text,B:text" {
		t.Fatal(v, err)
	}
	if ctx := newCtx(FindCommand("tables", "create"), v, nil, capture(nil).sys); ctx.Out.Field != "" {
		t.Fatal("a repeatable --field must not select an output path")
	}
}

func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	env := config.Env{"GC_CONFIG_DIR": dir}
	c := capture(env)
	if Run([]string{"--help"}, c.sys) != 0 || !strings.Contains(c.out.String(), "Usage: gc <command>") {
		t.Fatal("help")
	}
	c = capture(env)
	if Run([]string{"tasks", "list", "--bogus"}, c.sys) != 2 || !strings.Contains(c.err.String(), "Unknown option '--bogus'") {
		t.Fatal(c.err.String())
	}
	c = capture(env)
	if Run([]string{"context"}, c.sys) != 3 || !strings.Contains(c.err.String(), "GROUNDCONTROL is not connected on this machine (no API key)") {
		t.Fatal(c.err.String())
	}
	c = capture(config.Env{"GC_CONFIG_DIR": dir, "GC_API_URL": "http://localhost:3011/api/v1"})
	if Run([]string{"tasks", "list"}, c.sys) != 3 || !strings.Contains(c.err.String(), "registers at http://localhost:3011 ") {
		t.Fatal(c.err.String())
	}
	for _, args := range [][]string{{"comment", "id", "a", "b"}, {"tasks", "get", "x", "--json", "--field", "a"}, {"tasks", "--json"}, {"--json"}, {"help", "nope"}, {"tables", "delete", "t"}} {
		c = capture(env)
		if code := Run(args, c.sys); code != 2 {
			t.Fatalf("%v → %d (%s)", args, code, c.err.String())
		}
	}
	c = capture(config.Env{"GC_CONFIG_DIR": dir, "GC_PROFILE": "nope"})
	if Run([]string{"context"}, c.sys) != 1 || !strings.Contains(c.err.String(), `Unknown profile "nope"`) {
		t.Fatal(c.err.String())
	}
}

func TestEveryCommandHasSummaryAndExample(t *testing.T) {
	for _, c := range Commands {
		if len(c.Summary) <= 5 || len(c.Examples) == 0 || c.Run == nil {
			t.Fatalf("gc %s", strings.Join(c.Path, " "))
		}
	}
}

func TestSkillsEmbeddedAndThin(t *testing.T) {
	skills := Skills()
	if len(skills) != 2 || skills[0].Name != "groundcontrol" || skills[1].Name != "groundcontrol-datasheets" {
		t.Fatalf("skills %v", skills)
	}
	for _, s := range skills {
		file, err := os.ReadFile(filepath.Join("..", "..", "skills", s.Name, "SKILL.md"))
		if err != nil || string(file) != s.Content {
			t.Fatalf("%s: embedded copy differs", s.Name)
		}
		if !strings.HasPrefix(s.Content, "---\nname: "+s.Name+"\n") || len(skillDescription(s.Content)) < 40 {
			t.Fatalf("%s: frontmatter", s.Name)
		}
		if len(s.Content) >= 3000 {
			t.Fatalf("%s: %d bytes — skills stay thin bootstraps", s.Name, len(s.Content))
		}
	}
}

func TestSkillsInstall(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	env := config.Env{"HOME": home, "GC_CONFIG_DIR": t.TempDir()}
	c := capture(env)
	if Run([]string{"skills", "install"}, c.sys) != 0 {
		t.Fatal(c.err.String())
	}
	p := filepath.Join(home, ".claude", "skills", "groundcontrol", "SKILL.md")
	if b, _ := os.ReadFile(p); string(b) != Skills()[0].Content {
		t.Fatal("not installed")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		t.Fatal("all = only agents that exist")
	}
	os.WriteFile(p, []byte("edited"), 0o644)
	c = capture(env)
	if Run([]string{"skills", "install", "--agent", "claude"}, c.sys) != 0 || !strings.Contains(c.out.String(), "skipped") {
		t.Fatal(c.out.String())
	}
	if b, _ := os.ReadFile(p); string(b) != "edited" {
		t.Fatal("overwritten without --force")
	}
	c = capture(env)
	if Run([]string{"skills", "install", "--agent", "claude", "--force"}, c.sys) != 0 || !strings.Contains(c.out.String(), "updated") {
		t.Fatal(c.out.String())
	}
	if b, _ := os.ReadFile(p); string(b) != Skills()[0].Content {
		t.Fatal("not updated")
	}
	c = capture(config.Env{"HOME": t.TempDir()})
	if Run([]string{"skills", "install"}, c.sys) != 1 {
		t.Fatal("no agent home → error")
	}
	dir := t.TempDir()
	c = capture(env)
	if Run([]string{"skills", "install", "--dir", dir}, c.sys) != 0 {
		t.Fatal(c.err.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "groundcontrol-datasheets", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
