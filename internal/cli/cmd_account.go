package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/api"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

var whitespace = regexp.MustCompile(`\s`)

func accountCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"onboarding"},
			Summary: "Connect gc to a workspace with an agent API key and save it as a profile",
			Options: []OptSpec{
				{Name: "skip-verify", Bool: true, Desc: "Save without calling /me (offline setup)"},
			},
			Details: strings.Join([]string{
				"The key comes from --token <key>, --token - (stdin), an interactive hidden prompt (TTY),",
				"or stdin when nothing is attached. It is verified with /me and stored in config.json",
				"(dir 0700, file 0600). The profile is named after the workspace unless --profile is given,",
				"and becomes the current one. The key is never printed.",
			}, "\n"),
			Examples: []string{
				"gc onboarding --token gc_live_…",
				`printf %s "$KEY" | gc onboarding --token -`,
				"gc onboarding --profile acme --token gc_live_…",
			},
			Run: runOnboarding,
		},
		{
			Path:     []string{"profiles"},
			Summary:  "List saved profiles (workspaces); * marks the current one",
			Examples: []string{"gc profiles", "gc profiles use acme", "gc profiles remove old-workspace"},
			Run: func(c *Ctx) error {
				env := c.Sys.Env
				cfg, err := config.Load(env)
				if err != nil {
					return err
				}
				active := env["GC_PROFILE"]
				if active == "" {
					active = cfg.Current
				}
				rows := []any{}
				names := cfg.Profiles.Keys()
				for _, n := range names {
					p := cfg.Profile(n)
					rows = append(rows, js.O(
						"name", n, "current", n == cfg.Current,
						"workspace", js.Get(p, "workspace"), "agent", js.Get(p, "agent"), "api_url", js.Get(p, "api_url"),
						"key", config.RedactKey(config.Field(p, "api_key")), "created_at", js.Get(p, "created_at"),
					))
				}
				var current any
				if cfg.Current != "" {
					current = cfg.Current
				}
				return c.Out.Emit(js.O("current", current, "config", config.Path(env), "profiles", rows), func() []string {
					if len(rows) == 0 {
						return lines("No profiles. Run: gc onboarding --token gc_live_…")
					}
					ls := []string{}
					for _, r := range rows {
						name := js.S(js.Get(r, "name"))
						mark := " "
						if name == active {
							mark = "*"
						}
						dash := func(v any) string {
							if js.Truthy(v) {
								return js.Str(v)
							}
							return "-"
						}
						ls = append(ls, fmt.Sprintf("%s %s  %s  as %s  %s  %s", mark, name, dash(js.Get(r, "workspace")), dash(js.Get(r, "agent")), js.S(js.Get(r, "key")), js.Str(js.Get(r, "api_url"))))
					}
					if env["GC_PROFILE"] != "" {
						ls = append(ls, fmt.Sprintf("(GC_PROFILE=%s selects the profile in this shell)", env["GC_PROFILE"]))
					}
					if env["GC_API_KEY"] != "" {
						ls = append(ls, "(GC_API_KEY is set and overrides every profile key in this shell)")
					}
					return ls
				})
			},
		},
		{
			Path:     []string{"profiles", "use"},
			Summary:  "Make a profile the current one",
			Args:     "<name>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc profiles use acme"},
			Run: func(c *Ctx) error {
				cfg, err := config.Load(c.Sys.Env)
				if err != nil {
					return err
				}
				n := c.Args[0]
				if cfg.Profile(n) == nil {
					known := strings.Join(cfg.Profiles.Keys(), ", ")
					if known == "" {
						known = "(none)"
					}
					return clierr.New(fmt.Sprintf("Unknown profile \"%s\". Known: %s.", n, known))
				}
				cfg.Current = n
				if _, err := config.Save(cfg, c.Sys.Env); err != nil {
					return err
				}
				return c.Out.Emit(js.O("current", n), func() []string {
					return lines(fmt.Sprintf("Current profile: %s (%s)", n, js.Str(js.Get(cfg.Profile(n), "workspace"))))
				})
			},
		},
		{
			Path:     []string{"profiles", "remove"},
			Summary:  "Delete a saved profile (the key stays valid on the server — revoke it there)",
			Args:     "<name>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc profiles remove old-workspace"},
			Run: func(c *Ctx) error {
				cfg, err := config.Load(c.Sys.Env)
				if err != nil {
					return err
				}
				n := c.Args[0]
				if cfg.Profile(n) == nil {
					return clierr.New(fmt.Sprintf("Unknown profile \"%s\".", n))
				}
				cfg.Profiles.Delete(n)
				if cfg.Current == n {
					cfg.Current = ""
					if keys := cfg.Profiles.Keys(); len(keys) > 0 {
						cfg.Current = keys[0]
					}
				}
				if _, err := config.Save(cfg, c.Sys.Env); err != nil {
					return err
				}
				var current any
				shown := "(none)"
				if cfg.Current != "" {
					current, shown = cfg.Current, cfg.Current
				}
				return c.Out.Emit(js.O("removed", n, "current", current), func() []string {
					return lines(fmt.Sprintf("Removed %s. Current: %s", n, shown))
				})
			},
		},
	}
}

func runOnboarding(c *Ctx) error {
	sys := c.Sys
	token, given := c.Values["token"].(string)
	var err error
	switch {
	case (given && token == "-") || (!given && !sys.StdinIsTTY):
		token, err = sys.readStdin()
	case !given:
		if sys.PromptHidden == nil {
			return clierr.Usage("No API key given.", "Run: gc onboarding --token gc_live_…   (create one in GROUNDCONTROL → Settings → API Keys)")
		}
		token, err = sys.PromptHidden("GROUNDCONTROL API key (gc_live_…): ")
	}
	if err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return clierr.Usage("No API key given.", "Run: gc onboarding --token gc_live_…   (create one in GROUNDCONTROL → Settings → API Keys)")
	}
	if whitespace.MatchString(token) {
		return clierr.Usage("The API key contains whitespace — paste only the key.")
	}

	cfg, err := config.Load(sys.Env)
	if err != nil {
		return err
	}
	requested, _ := c.str("profile")
	existing := cfg.Profile(requested)
	apiURL, _ := c.str("api-url")
	for _, alt := range []string{sys.Env["GC_API_URL"], config.Field(existing, "api_url"), config.DefaultAPIURL} {
		if apiURL == "" {
			apiURL = alt
		}
	}
	apiURL = strings.TrimRight(apiURL, "/")

	workspace := config.Field(existing, "workspace")
	agent := config.Field(existing, "agent")
	var workflow any = js.Undefined
	skip := c.bool("skip-verify")
	if !skip {
		me, err := api.New(apiURL, token, "").GetMe()
		if err != nil {
			if clierr.IsUnauthorized(err) {
				return &clierr.Error{
					Msg:  fmt.Sprintf("The API key was rejected by %s (401). Nothing was saved.", apiURL),
					Hint: "Check the key (GROUNDCONTROL → Settings → API Keys) — it may be revoked or from another server (--api-url).",
					Code: 3,
				}
			}
			return clierr.New(fmt.Sprintf("Key check failed against %s: %s", apiURL, err), "Check the key (Settings → API Keys) and --api-url. Nothing was saved.")
		}
		workspace = js.S(js.Get(me, "data", "tenant", "name"))
		agent = js.S(js.Get(me, "data", "user", "display_name"))
		workflow = js.Get(me, "data", "tenant", "workflow")
		if js.Get(me, "data", "user", "is_agent") == false {
			c.Out.Err("warning: this key belongs to a human member, not an agent.")
		}
	} else if requested == "" {
		return clierr.Usage("--skip-verify needs --profile <name> (the workspace name is unknown without /me).")
	}

	profileName := requested
	if profileName == "" {
		profileName = config.Slugify(workspace)
	}
	cfg.Profiles.Set(profileName, config.NewProfile(apiURL, token, workspace, agent))
	cfg.Current = profileName
	path, err := config.Save(cfg, sys.Env)
	if err != nil {
		return err
	}

	result := js.O("profile", profileName, "workspace", workspace, "agent", agent, "workflow", js.Or(workflow, nil),
		"api_url", apiURL, "key", config.RedactKey(token), "config", path)
	return c.Out.Emit(result, func() []string {
		first := "Saved (not verified)."
		if !skip {
			wf := ""
			if js.Truthy(workflow) {
				wf = " (" + js.Str(workflow) + ")"
			}
			first = fmt.Sprintf("Connected to workspace \"%s\" as \"%s\"%s.", workspace, agent, wf)
		}
		note := ""
		if sys.Env["GC_API_KEY"] != "" {
			note = "note: GC_API_KEY is set in this shell and overrides the saved profile."
		}
		return keepNonEmpty([]string{
			first,
			fmt.Sprintf("Profile: %s (current) · key %s · %s", profileName, config.RedactKey(token), apiURL),
			"Config:  " + path,
			note,
			"Next:    gc context",
		})
	})
}
