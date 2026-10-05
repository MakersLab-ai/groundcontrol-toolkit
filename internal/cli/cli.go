// Package cli is the gc command table, its argument parser, help and the
// entry point Run.
package cli

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

// Commands is the full command table (order = help order within a group).
var Commands []*Command

func init() {
	Commands = concat(
		accountCommands(),
		contextCommands(),
		[]*Command{listenCommand()},
		taskCommands(),
		docCommands(),
		initiativeCommands(),
		searchCommands(),
		goalCommands(),
		tableCommands(),
		journalCommands(),
		guideCommands(),
		skillCommands(),
		[]*Command{versionCommand()},
	)
}

func concat(groups ...[]*Command) []*Command {
	out := []*Command{}
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func versionCommand() *Command {
	return &Command{
		Path:     []string{"version"},
		Summary:  "Print the gc version",
		Examples: []string{"gc version"},
		Run: func(c *Ctx) error {
			return c.Out.Emit(js.O("version", c.Sys.Version), func() []string { return lines("gc " + c.Sys.Version) })
		},
	}
}

var groupSummary = map[string]string{
	"tasks":       "Tasks: list, get, create, update",
	"docs":        "Documents: list, get, create, update, archive, comment",
	"initiatives": "Initiatives: list, get, memory",
	"goals":       "Goals (OKRs): list, get, create, update, kr-update, checkin-submit",
	"tables":      "Datasheets: list, get, rows, add-rows, update-rows, … (gc tables --help)",
	"journal":     "Journal: list, get, summary",
	"skills":      "Agent skills: list, install",
	"profiles":    "Saved workspaces: list, use, remove",
}

var sections = []struct {
	title string
	names []string
}{
	{"Start", []string{"onboarding", "context", "changes", "listen", "guide"}},
	{"Work", []string{"tasks", "comment", "attach", "docs", "search", "semantic-search", "initiatives", "members", "goals", "tables", "journal"}},
	{"Setup", []string{"profiles", "skills", "version", "help"}},
}

func isGroup(word string) bool {
	for _, c := range Commands {
		if len(c.Path) > 1 && c.Path[0] == word {
			return true
		}
	}
	return false
}

// FindCommand returns the command with exactly this path.
func FindCommand(path ...string) *Command {
	for _, c := range Commands {
		if len(c.Path) == len(path) {
			match := true
			for i := range path {
				if c.Path[i] != path[i] {
					match = false
					break
				}
			}
			if match {
				return c
			}
		}
	}
	return nil
}

// positionalIndices finds the leading command words in argv, skipping global
// flags and their values — global flags may come before the command.
func positionalIndices(argv []string) []int {
	idx := []int{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if strings.HasPrefix(a, "--") {
				if o, ok := globalOption(a[2:]); ok && !o.Bool {
					i++
				}
			}
			continue
		}
		idx = append(idx, i)
		if len(idx) == 2 {
			break
		}
	}
	return idx
}

type resolved struct {
	command *Command
	group   string
	rest    []string
}

var subcommandLike = regexp.MustCompile(`^[a-z][a-z-]*$`)

func resolveCommand(argv []string) (resolved, error) {
	idx := positionalIndices(argv)
	drop := func(n int) []string {
		skip := map[int]bool{}
		for _, i := range idx[:min(n, len(idx))] {
			skip[i] = true
		}
		out := []string{}
		for i, a := range argv {
			if !skip[i] {
				out = append(out, a)
			}
		}
		return out
	}
	if len(idx) == 0 {
		return resolved{rest: argv}, nil
	}
	w0 := argv[idx[0]]
	w1, has1 := "", len(idx) > 1
	if has1 {
		w1 = argv[idx[1]]
	}
	if w0 == "help" {
		return resolved{group: "help", rest: drop(1)}, nil
	}
	if isGroup(w0) {
		if has1 {
			if sub := FindCommand(w0, w1); sub != nil {
				return resolved{command: sub, rest: drop(2)}, nil
			}
		}
		// e.g. `gc profiles` lists
		if solo := FindCommand(w0); solo != nil && (!has1 || !subcommandLike.MatchString(w1)) {
			return resolved{command: solo, rest: drop(1)}, nil
		}
		if has1 {
			return resolved{}, clierr.Usage(fmt.Sprintf(`Unknown command "gc %s %s".`, w0, w1), "Run: gc "+w0+" --help")
		}
		return resolved{group: w0, rest: drop(1)}, nil
	}
	cmd := FindCommand(w0)
	if cmd == nil {
		return resolved{}, clierr.Usage(fmt.Sprintf("Unknown command \"%s\".", w0), "Run: gc help")
	}
	return resolved{command: cmd, rest: drop(1)}, nil
}

// parseCommandArgs parses flags and positionals the way node's util.parseArgs
// (strict, positionals allowed) does: flags anywhere, `--x=v` or `--x v`,
// `--` ends the flags, a lone `-` is a positional.
func parseCommandArgs(cmd *Command, rest []string) (Values, []string, error) {
	usage := "Usage: gc " + strings.TrimRight(strings.Join(cmd.Path, " ")+" "+cmd.Args, " ")
	parseErr := func(msg string) error {
		return clierr.Usage(msg, "Run: gc "+strings.Join(cmd.Path, " ")+" --help")
	}
	// A command's own option shadows a global one of the same name
	// (`gc tables create --field` is a column, not the output path).
	lookup := func(name string) (OptSpec, bool) {
		if o, ok := cmd.option(name); ok {
			return o, true
		}
		return globalOption(name)
	}
	values := Values{}
	positionals := []string{}
	set := func(o OptSpec, v string) {
		if o.Multiple {
			prev, _ := values[o.Name].([]string)
			values[o.Name] = append(prev, v)
		} else {
			values[o.Name] = v
		}
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			positionals = append(positionals, rest[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		var name, raw string
		var inline *string
		if strings.HasPrefix(a, "--") {
			body := a[2:]
			if eq := strings.IndexByte(body, '='); eq >= 0 {
				v := body[eq+1:]
				inline = &v
				body = body[:eq]
			}
			name, raw = body, "--"+body
		} else {
			// short option(s): only -h exists
			short := a[1:2]
			raw = "-" + short
			var found *OptSpec
			for _, o := range append(append([]OptSpec{}, cmd.Options...), GlobalOptions...) {
				if o.Short == short {
					found = &o
					break
				}
			}
			if found == nil || len(a) > 2 {
				if found != nil && len(a) > 2 {
					raw = "-" + a[2:3]
				}
				return nil, nil, parseErr(fmt.Sprintf("Unknown option '%s'.", raw))
			}
			name = found.Name
		}
		o, ok := lookup(name)
		if !ok {
			return nil, nil, parseErr(fmt.Sprintf("Unknown option '%s'.", raw))
		}
		if o.Bool {
			if inline != nil {
				return nil, nil, parseErr(fmt.Sprintf("Option '%s' does not take an argument", raw))
			}
			values[o.Name] = true
			continue
		}
		if inline != nil {
			set(o, *inline)
			continue
		}
		if i+1 >= len(rest) {
			return nil, nil, parseErr(fmt.Sprintf("Option '%s <value>' argument missing", raw))
		}
		v := rest[i+1]
		if len(v) > 1 && v[0] == '-' {
			return nil, nil, parseErr(fmt.Sprintf("Option '%s' argument is ambiguous.\nDid you forget to specify the option argument for '%s'?\nTo specify an option argument starting with a dash use '%s=-XYZ'.", raw, raw, raw))
		}
		set(o, v)
		i++
	}
	if values["help"] == true {
		return values, positionals, nil
	}
	n := len(positionals)
	if n < cmd.MinArgs {
		msg := "Missing argument."
		if cmd.Args != "" {
			msg = "Missing argument: " + cmd.Args + "."
		}
		return nil, nil, clierr.Usage(msg, usage)
	}
	if n > cmd.MaxArgs {
		hint := usage
		for _, p := range cmd.Path {
			if p == "comment" {
				hint += " — quote the body, or use --body-file -"
				break
			}
		}
		return nil, nil, clierr.Usage(fmt.Sprintf("Unexpected argument \"%s\".", positionals[cmd.MaxArgs]), hint)
	}
	return values, positionals, nil
}

// ─── help ─────────────────────────────────────────────────────────────────────

func flagLines(specs []OptSpec) []string {
	type row struct{ a, b string }
	rows := []row{}
	w := 0
	for _, s := range specs {
		a := "--" + s.Name
		if s.Short != "" {
			a = "-" + s.Short + ", " + a
		}
		if s.Value != "" {
			a += " " + s.Value
		}
		b := s.Desc
		if s.Multiple {
			b += " (repeatable)"
		}
		rows = append(rows, row{a, b})
		w = max(w, utf8.RuneCountInString(a))
	}
	w = min(36, w)
	out := []string{}
	for _, r := range rows {
		out = append(out, "  "+padEnd(r.a, w)+"  "+r.b)
	}
	return out
}

func padEnd(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	return s
}

func commandHelp(cmd *Command) string {
	path := strings.Join(cmd.Path, " ")
	usage := "Usage: gc " + path
	if cmd.Args != "" {
		usage += " " + cmd.Args
	}
	if len(cmd.Options) > 0 {
		usage += " [flags]"
	}
	ls := []string{"gc " + path + " — " + cmd.Summary, "", usage}
	if cmd.Details != "" {
		ls = append(ls, "", cmd.Details)
	}
	if len(cmd.Options) > 0 {
		ls = append(ls, "", "Flags:")
		ls = append(ls, flagLines(cmd.Options)...)
	}
	if len(cmd.Examples) > 0 {
		ls = append(ls, "", "Examples:")
		for _, e := range cmd.Examples {
			ls = append(ls, "  "+e)
		}
	}
	ls = append(ls, "", "Global flags: --json, --field <path>, --profile <name>, --token <key>, --api-url <url>, --session-id <id> (gc help globals)")
	return strings.Join(ls, "\n")
}

func groupHelp(group string) string {
	subs := []*Command{}
	w := 0
	for _, c := range Commands {
		if c.Path[0] == group {
			subs = append(subs, c)
			w = max(w, utf8.RuneCountInString(strings.Join(c.Path, " ")))
		}
	}
	ls := []string{"gc " + group + " — " + groupSummary[group], ""}
	for _, c := range subs {
		ls = append(ls, "  gc "+padEnd(strings.Join(c.Path, " "), w)+"  "+c.Summary)
	}
	ls = append(ls, "", "Help for one: gc "+group+" <command> --help")
	return strings.Join(ls, "\n")
}

func mainHelp(version string) string {
	summaryOf := func(name string) string {
		if s, ok := groupSummary[name]; ok {
			return s
		}
		if c := FindCommand(name); c != nil {
			return c.Summary
		}
		if name == "help" {
			return "Help for a command: gc help tasks create"
		}
		return ""
	}
	ls := []string{"gc " + version + " — GROUNDCONTROL for agents (alias: groundcontrol)", "", "Usage: gc <command> [flags]"}
	for _, s := range sections {
		ls = append(ls, "", s.title+":")
		for _, n := range s.names {
			ls = append(ls, "  "+padEnd(n, 16)+" "+summaryOf(n))
		}
	}
	ls = append(ls, "", "Global flags:")
	ls = append(ls, flagLines(GlobalOptions)...)
	ls = append(ls,
		"",
		"Examples:",
		"  gc onboarding --token gc_live_…",
		"  gc context",
		"  gc changes --since 2h",
		"  gc tasks get <id>",
		"  gc comment <id> --body-file - < result.md",
		"  gc guide tasks",
		"",
		"Environment: GC_API_KEY, GC_API_URL, GC_PROFILE, GC_SESSION_ID, GC_CONFIG_DIR",
		"",
		"Exit codes: 0 ok · 1 API or other error · 2 usage error · 3 not connected (no API key, or the key was rejected)",
	)
	return strings.Join(ls, "\n")
}

// ─── entry ────────────────────────────────────────────────────────────────────

// Run runs one invocation and returns the exit code (0 ok, 1 failure, 2 usage, 3 not connected).
func Run(argv []string, sys *Sys) int {
	out := func(s string) {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		io.WriteString(sys.Stdout, s)
	}
	errOut := func(s string) {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		io.WriteString(sys.Stderr, s)
	}
	err := run(argv, sys, out)
	if err == nil {
		return 0
	}
	var se *silentError
	if errors.As(err, &se) {
		return se.code
	}
	var ce *clierr.Error
	if errors.As(err, &ce) {
		errOut("gc: " + ce.Msg)
		if ce.Hint != "" {
			errOut(ce.Hint)
		}
		return ce.Code
	}
	errOut("gc: " + err.Error())
	return 1
}

func run(argv []string, sys *Sys, out func(string)) error {
	if len(argv) == 0 || (len(argv) == 1 && (argv[0] == "--help" || argv[0] == "-h")) {
		out(mainHelp(sys.Version))
		return nil
	}
	if len(argv) == 1 && (argv[0] == "--version" || argv[0] == "-v") {
		out("gc " + sys.Version)
		return nil
	}
	r, err := resolveCommand(argv)
	if err != nil {
		return err
	}
	if r.group == "help" {
		words := []string{}
		for _, a := range r.rest {
			if !strings.HasPrefix(a, "-") {
				words = append(words, a)
			}
		}
		switch {
		case len(words) > 0 && words[0] == "globals":
			out(strings.Join(append([]string{"Global flags (any command):"}, flagLines(GlobalOptions)...), "\n"))
		case len(words) == 0:
			out(mainHelp(sys.Version))
		default:
			var cmd *Command
			if len(words) >= 2 {
				cmd = FindCommand(words[:2]...)
			}
			if cmd == nil {
				cmd = FindCommand(words[0])
			}
			if cmd != nil {
				out(commandHelp(cmd))
			} else if isGroup(words[0]) {
				out(groupHelp(words[0]))
			} else {
				return clierr.Usage(fmt.Sprintf("Unknown command \"%s\".", strings.Join(words, " ")), "Run: gc help")
			}
		}
		return nil
	}
	if r.group != "" {
		out(groupHelp(r.group))
		// `gc tasks` with no subcommand: help is the answer, not an error.
		for _, a := range r.rest {
			if a == "--help" || a == "-h" {
				return nil
			}
		}
		if len(r.rest) == 0 {
			return nil
		}
		return silentExit(2)
	}
	if r.command == nil {
		// Only flags, no command (e.g. `gc --json`).
		return clierr.Usage("No command given.", "Run: gc help")
	}
	cmd := r.command
	values, positionals, err := parseCommandArgs(cmd, r.rest)
	if err != nil {
		return err
	}
	if values["help"] == true {
		if isGroup(cmd.Path[0]) && len(cmd.Path) == 1 {
			out(groupHelp(cmd.Path[0]) + "\n\n" + commandHelp(cmd))
		} else {
			out(commandHelp(cmd))
		}
		return nil
	}
	if values["json"] == true {
		if _, ok := values["field"].(string); ok {
			return clierr.Usage("Use either --json or --field, not both.")
		}
	}
	ctx := newCtx(cmd, values, positionals, sys)
	if err := cmd.Run(ctx); err != nil {
		// A rejected key is "not connected", wherever it surfaces (onboarding has its own message).
		if clierr.IsUnauthorized(err) && clierr.Code(err) != 3 {
			apiURL := ""
			if a, aerr := ctx.Auth(); aerr == nil {
				apiURL = a.APIURL
			}
			return clierr.NotConnected(clierr.Rejected, apiURL)
		}
		return err
	}
	return nil
}

// silentExit ends the run with a code and no message (the output was already printed).
func silentExit(code int) error { return &silentError{code} }

type silentError struct{ code int }

func (e *silentError) Error() string { return "" }
