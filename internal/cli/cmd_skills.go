package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toolkit "github.com/MakersLab-ai/groundcontrol-toolkit"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

// Skill is one embedded skills/<name>/SKILL.md.
type Skill struct {
	Name    string
	Content string
}

// Skills lists the skills compiled into the binary, in directory order.
func Skills() []Skill {
	entries, err := fs.ReadDir(toolkit.Skills, "skills")
	if err != nil {
		panic(err)
	}
	out := []Skill{}
	for _, e := range entries {
		b, err := fs.ReadFile(toolkit.Skills, "skills/"+e.Name()+"/SKILL.md")
		if err != nil {
			continue
		}
		out = append(out, Skill{Name: e.Name(), Content: string(b)})
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
				for _, s := range Skills() {
					rows = append(rows, js.O("name", s.Name, "bytes", len(s.Content), "description", skillDescription(s.Content)))
				}
				return c.Out.Emit(rows, func() []string {
					ls := []string{}
					for _, r := range rows {
						ls = append(ls, fmt.Sprintf("%s  (%s B)\n  %s", js.Str(js.Get(r, "name")), js.Str(js.Get(r, "bytes")), js.Str(js.Get(r, "description"))))
					}
					return ls
				})
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
			Details:  "Writes ~/.claude/skills/<name>/SKILL.md (and ~/.codex/…, ~/.openclaw/…). With --agent all, only agents whose home dir exists.",
			Examples: []string{"gc skills install", "gc skills install --agent claude --force", "gc skills install --dir ./.claude/skills"},
			Run:      runSkillsInstall,
		},
	}
}

func runSkillsInstall(c *Ctx) error {
	home := config.Home(c.Sys.Env)
	agent, ok := c.str("agent")
	if !ok {
		agent = "all"
	}
	type target struct{ agent, dir string }
	var targets []target
	if dir, ok := c.str("dir"); ok {
		targets = []target{{"dir", dir}}
	} else if agent == "all" {
		for _, a := range agentDirs {
			if fileExists(filepath.Join(home, a.home)) {
				targets = append(targets, target{a.agent, filepath.Join(home, a.skills)})
			}
		}
		if len(targets) == 0 {
			return clierr.New("No Claude Code, Codex or OpenClaw home directory found.", "Pass --agent claude|codex|openclaw or --dir <path>.")
		}
	} else {
		for _, a := range agentDirs {
			if a.agent == agent {
				targets = []target{{agent, filepath.Join(home, a.skills)}}
			}
		}
		if targets == nil {
			return clierr.Usage(fmt.Sprintf("Unknown --agent \"%s\". Use claude, codex, openclaw or all.", agent))
		}
	}

	results := []any{}
	skipped := false
	for _, t := range targets {
		for _, s := range Skills() {
			path := filepath.Join(t.dir, s.Name, "SKILL.md")
			status := "installed"
			if cur, err := os.ReadFile(path); err == nil {
				switch {
				case string(cur) == s.Content:
					status = "unchanged"
				case c.bool("force"):
					status = "updated"
				default:
					status = "skipped"
					skipped = true
				}
			}
			if status == "installed" || status == "updated" {
				if err := os.MkdirAll(filepath.Join(t.dir, s.Name), 0o777); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(s.Content), 0o666); err != nil {
					return err
				}
			}
			results = append(results, js.O("agent", t.agent, "skill", s.Name, "path", path, "status", status))
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
