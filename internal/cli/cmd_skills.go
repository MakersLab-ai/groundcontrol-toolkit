package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	toolkit "github.com/MakersLab-ai/groundcontrol-toolkit"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

// Skill is one embedded skills/<name>/SKILL.md. Variants holds per-agent
// overlays (skills/<name>/SKILL.<agent>.md): installed for that agent under
// the same skill name instead of the generic Content.
type Skill struct {
	Name     string
	Content  string
	Variants map[string]string // agent → content
}

// ContentFor is what gets installed for an agent: its variant, else the generic skill.
func (s Skill) ContentFor(agent string) string {
	if v, ok := s.Variants[agent]; ok {
		return v
	}
	return s.Content
}

// VariantAgents lists the agents with an overlay, sorted.
func (s Skill) VariantAgents() []string {
	out := []string{}
	for a := range s.Variants {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

var variantFile = regexp.MustCompile(`^SKILL\.([a-z0-9-]+)\.md$`)

// Skills lists the skills compiled into the binary, in directory order.
func Skills() []Skill {
	entries, err := fs.ReadDir(toolkit.Skills, "skills")
	if err != nil {
		panic(err)
	}
	out := []Skill{}
	for _, e := range entries {
		dir := "skills/" + e.Name()
		b, err := fs.ReadFile(toolkit.Skills, dir+"/SKILL.md")
		if err != nil {
			continue
		}
		s := Skill{Name: e.Name(), Content: string(b), Variants: map[string]string{}}
		files, _ := fs.ReadDir(toolkit.Skills, dir)
		for _, f := range files {
			if m := variantFile.FindStringSubmatch(f.Name()); m != nil {
				if v, err := fs.ReadFile(toolkit.Skills, dir+"/"+f.Name()); err == nil {
					s.Variants[m[1]] = string(v)
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// agentDirs: agent → its skills directory, relative to $HOME. The agent
// "exists" when its home dir does.
var agentDirs = []struct{ agent, home, skills string }{
	{"claude", ".claude", ".claude/skills"},
	{"codex", ".codex", ".codex/skills"},
	{"openclaw", ".openclaw", ".openclaw/skills"},
}

var descriptionRe = regexp.MustCompile(`(?m)^description:\s*(.+)$`)

func skillDescription(content string) string {
	if m := descriptionRe.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func skillCommands() []*Command {
	return []*Command{
		{
			Path:     []string{"skills", "list"},
			Summary:  "List the agent skills built into gc",
			Examples: []string{"gc skills list"},
			Run: func(c *Ctx) error {
				rows := []any{}
				texts := []string{}
				for _, s := range Skills() {
					variants := js.NewObject()
					size := fmt.Sprintf("%d B", len(s.Content))
					for _, a := range s.VariantAgents() {
						variants.Set(a, js.O("bytes", len(s.Variants[a])))
						size += fmt.Sprintf("; %s variant %d B", a, len(s.Variants[a]))
					}
					rows = append(rows, js.O("name", s.Name, "bytes", len(s.Content), "description", skillDescription(s.Content), "variants", variants))
					texts = append(texts, fmt.Sprintf("%s  (%s)\n  %s", s.Name, size, skillDescription(s.Content)))
				}
				return c.Out.Emit(rows, func() []string { return texts })
			},
		},
		{
			Path:    []string{"skills", "install"},
			Summary: "Install the gc skills for Claude Code, Codex and/or OpenClaw",
			Options: []OptSpec{
				{Name: "agent", Value: "<claude|codex|openclaw|all>", Desc: "Target agent (default all = every agent installed here)"},
				{Name: "dir", Value: "<path>", Desc: "Install into <path>/<skill>/SKILL.md instead"},
				{Name: "force", Bool: true, Desc: "Overwrite skills that differ from the built-in version"},
			},
			Details: strings.Join([]string{
				"Writes ~/.claude/skills/<name>/SKILL.md (and ~/.codex/…, ~/.openclaw/…). With --agent all, only agents whose home dir exists.",
				"An agent with its own variant of a skill (gc skills list) gets that variant under the same skill name.",
				"With --dir, --agent <name> picks that agent's variants.",
			}, "\n"),
			Examples: []string{"gc skills install", "gc skills install --agent claude --force", "gc skills install --dir ./.claude/skills"},
			Run:      runSkillsInstall,
		},
	}
}

// skillTarget is one skills directory; variant names the agent whose
// overlays apply there ("" = the generic skills).
type skillTarget struct{ agent, variant, dir string }

// installSkills writes every embedded skill into t.dir. An existing file that
// differs is overwritten only with force; dryRun reports without writing.
// Status per skill: installed | updated | unchanged | skipped.
func installSkills(t skillTarget, force, dryRun bool) ([]*js.Object, error) {
	results := []*js.Object{}
	for _, s := range Skills() {
		content := s.ContentFor(t.variant)
		path := filepath.Join(t.dir, s.Name, "SKILL.md")
		status := "installed"
		if cur, err := os.ReadFile(path); err == nil {
			switch {
			case string(cur) == content:
				status = "unchanged"
			case force:
				status = "updated"
			default:
				status = "skipped"
			}
		}
		if !dryRun && (status == "installed" || status == "updated") {
			if err := os.MkdirAll(filepath.Join(t.dir, s.Name), 0o777); err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
				return nil, err
			}
		}
		results = append(results, js.O("agent", t.agent, "skill", s.Name, "path", path, "status", status))
	}
	return results, nil
}

func runSkillsInstall(c *Ctx) error {
	home := config.Home(c.Sys.Env)
	agent, ok := c.str("agent")
	if !ok {
		agent = "all"
	}
	known := func(a string) bool {
		for _, d := range agentDirs {
			if d.agent == a {
				return true
			}
		}
		return false
	}
	if agent != "all" && !known(agent) {
		return clierr.Usage(fmt.Sprintf("Unknown --agent \"%s\". Use claude, codex, openclaw or all.", agent))
	}
	var targets []skillTarget
	if dir, ok := c.str("dir"); ok {
		variant := agent
		if variant == "all" {
			variant = ""
		}
		targets = []skillTarget{{"dir", variant, dir}}
	} else {
		for _, a := range agentDirs {
			if (agent == "all" && fileExists(filepath.Join(home, a.home))) || a.agent == agent {
				targets = append(targets, skillTarget{a.agent, a.agent, filepath.Join(home, a.skills)})
			}
		}
		if len(targets) == 0 {
			return clierr.New("No Claude Code, Codex or OpenClaw home directory found.", "Pass --agent claude|codex|openclaw or --dir <path>.")
		}
	}

	results := []any{}
	skipped := false
	for _, t := range targets {
		rs, err := installSkills(t, c.bool("force"), false)
		if err != nil {
			return err
		}
		for _, r := range rs {
			if js.Get(r, "status") == "skipped" {
				skipped = true
			}
			results = append(results, r)
		}
	}
	return c.Out.Emit(results, func() []string {
		ls := []string{}
		for _, r := range results {
			ls = append(ls, output.PadEnd(js.Str(js.Get(r, "status")), 9)+" "+js.Str(js.Get(r, "path")))
		}
		if skipped {
			ls = append(ls, "Skipped files differ from this gc version — rerun with --force to overwrite them.")
		}
		return ls
	})
}
