package cli

// The texts agents read — the skills and the README — name `gc` commands and
// flags. A renamed command or flag would leave them following instructions
// that exit 2, so every `gc …` snippet in them must name an existing command
// and only flags that command (or the global set) knows. Purely repo-internal:
// the server's guide is not checked here.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	inlineGc  = regexp.MustCompile("`(gc [^`\n]+)`")
	codeBlock = regexp.MustCompile("(?s)```[a-z]*\n(.*?)```")
	chain     = regexp.MustCompile(`\s+(?:·|&&|\|\||\||;)\s+`)
	comment   = regexp.MustCompile(`\s+#\s.*$`)
	quoted    = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	flagRe    = regexp.MustCompile(`--([a-z][a-z-]*)`)
)

// snippets returns every `gc …` invocation: inline code spans and code-block lines.
func snippets(text string) []string {
	raw := []string{}
	for _, m := range inlineGc.FindAllStringSubmatch(text, -1) {
		raw = append(raw, m[1])
	}
	for _, b := range codeBlock.FindAllStringSubmatch(text, -1) {
		for _, line := range strings.Split(b[1], "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "gc ") {
				raw = append(raw, comment.ReplaceAllString(t, ""))
			}
		}
	}
	out := []string{}
	// One span may chain several calls: `gc a · gc b`, `gc a && gc b`, `x | gc b`.
	for _, s := range raw {
		for _, part := range chain.Split(strings.ReplaceAll(s, `\`, ""), -1) {
			if part = strings.TrimSpace(part); strings.HasPrefix(part, "gc ") {
				out = append(out, part)
			}
		}
	}
	return out
}

// longest command path that prefixes the words
func resolveWords(words []string) *Command {
	var best *Command
	for _, c := range Commands {
		if len(c.Path) > len(words) {
			continue
		}
		match := true
		for i, p := range c.Path {
			if words[i] != p {
				match = false
				break
			}
		}
		if match && (best == nil || len(c.Path) > len(best.Path)) {
			best = c
		}
	}
	return best
}

func isPlaceholder(w string) bool {
	return w == "" || strings.HasPrefix(w, "<") || w == "…" || w == "..." || strings.HasPrefix(w, "…")
}

func checkSnippet(s string) string {
	words := strings.Fields(s)[1:]
	if len(words) == 0 || isPlaceholder(words[0]) {
		return ""
	}
	if words[0] == "help" || words[0] == "--help" || words[0] == "-h" || words[0] == "--version" {
		return ""
	}
	cmd := resolveWords(words)
	if cmd == nil {
		// A group mention (`gc tables …`) is fine while commands live under it.
		if (len(words) == 1 || isPlaceholder(words[1]) || strings.HasPrefix(words[1], "--")) && isGroup(words[0]) {
			return ""
		}
		return "names no gc command"
	}
	for _, m := range flagRe.FindAllStringSubmatch(quoted.ReplaceAllString(s, ""), -1) {
		if m[1] == "help" {
			continue
		}
		if _, ok := cmd.option(m[1]); ok {
			continue
		}
		if _, ok := globalOption(m[1]); !ok {
			return "--" + m[1] + " is not a flag of gc " + strings.Join(cmd.Path, " ")
		}
	}
	return ""
}

func TestSnippetsNameRealCommandsAndFlags(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "skills", "*", "SKILL.md"))
	files = append(files, filepath.Join("..", "..", "README.md"))
	if len(files) < 3 {
		t.Fatalf("found only %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		found := snippets(string(b))
		if len(found) == 0 {
			t.Fatalf("%s: no gc snippets found — is the extractor broken?", f)
		}
		for _, s := range found {
			if msg := checkSnippet(s); msg != "" {
				t.Errorf("%s: %q %s", f, s, msg)
			}
		}
	}
}

func TestSnippetCheckerCatchesDrift(t *testing.T) {
	bad := map[string]string{
		"gc tasks frobnicate":          "names no gc command",
		"gc tasks list --assignee me":  "--assignee is not a flag of gc tasks list",
		"gc changes --cursor x --json": "--cursor is not a flag of gc changes",
		"gc nope":                      "names no gc command",
	}
	for s, want := range bad {
		if got := checkSnippet(s); got != want {
			t.Fatalf("%q → %q, want %q", s, got, want)
		}
	}
	for _, ok := range []string{"gc tables …", "gc tasks get <id> --since 2h --json", "gc help tasks create", "gc profiles use acme", `gc listen --exec 'x --whatever'`} {
		if got := checkSnippet(ok); got != "" {
			t.Fatalf("%q → %q", ok, got)
		}
	}
	got := snippets("Run `gc a · gc b` and\n```sh\ngc c --x   # comment --y\nfoo | gc d\n```\n")
	if strings.Join(got, "|") != "gc a|gc b|gc c --x" {
		t.Fatalf("%v", got)
	}
}
