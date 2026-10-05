package cli

import (
	"fmt"
	"math"
	"strings"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

func goalLine(g any) string {
	pct := math.Floor(js.Number(js.Or(js.Get(g, "progress"), 0.0)) + 0.5) // Math.round
	owner, agent, initiative := "", "", ""
	if n := output.Name(js.Get(g, "owner")); js.Truthy(n) {
		owner = "owner " + js.Str(n)
	}
	if n := output.Name(js.Get(g, "agent")); js.Truthy(n) {
		agent = "agent " + js.Str(n)
	}
	if n := js.Get(g, "initiative", "name"); js.Truthy(n) {
		initiative = "#" + js.Str(n)
	}
	return output.Join("  ",
		js.JoinStr(js.Get(g, "id")),
		output.Pad(js.Get(g, "status"), 8),
		output.PadStart(js.NumberString(pct)+"%", 4),
		js.JoinStr(js.Get(g, "title")),
		initiative, owner, agent,
	)
}

// parseKr reads `--kr "Title|target|unit"` (target/unit optional).
func parseKr(spec string) (*js.Object, error) {
	parts := strings.Split(spec, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	at := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	title, target, unit := at(0), at(1), at(2)
	if title == "" {
		return nil, clierr.Usage(fmt.Sprintf("--kr \"%s\": the title is empty. Format: \"Title|target|unit\".", spec))
	}
	var t any = js.Undefined
	if target != "" {
		n := js.ParseNumber(target)
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, clierr.Usage(fmt.Sprintf("--kr \"%s\": target \"%s\" is not a number.", spec, target))
		}
		t = n
	}
	var u any = js.Undefined
	if unit != "" {
		u = unit
	}
	return js.O("title", title, "target_value", t, "unit", u), nil
}

var goalFlags = []OptSpec{
	{Name: "description", Value: "<md>", Desc: "Description"},
	{Name: "description-file", Value: "<path|->", Desc: "Read the description from a file, or stdin with -"},
	{Name: "initiative", Value: "<id>", Desc: "Home initiative (sets who can see the goal)"},
	{Name: "owner", Value: "<member-id>", Desc: "Human who owns the goal"},
	{Name: "agent", Value: "<member-id>", Desc: "Agent that runs the weekly check-ins"},
	{Name: "autonomy", Value: "<approve|act>", Desc: "approve (default): planned tasks wait in backlog; act: start right away"},
}

func opts(groups ...[]OptSpec) []OptSpec {
	out := []OptSpec{}
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func goalCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"goals", "list"},
			Summary: "List goals with progress",
			Options: []OptSpec{
				{Name: "status", Value: "<s>", Desc: "active | achieved | missed | archived"},
				{Name: "initiative", Value: "<id>", Desc: "Only this initiative"},
				{Name: "mine", Bool: true, Desc: "Only goals where I am the agent"},
			},
			Examples: []string{"gc goals list", "gc goals list --mine"},
			Run: func(c *Ctx) error {
				var agent any = js.Undefined
				if c.bool("mine") {
					agent = "me"
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				fetched, err := client.ListObjectives(js.O("status", c.sv("status"), "agent", agent, "limit", "100"))
				if err != nil {
					return err
				}
				// The route has no initiative filter; narrow here.
				initiative, hasInitiative := c.str("initiative")
				res := fetched
				if hasInitiative {
					kept := []any{}
					for _, g := range js.List(js.Get(fetched, "data")) {
						if js.Get(g, "initiative_id") == initiative {
							kept = append(kept, g)
						}
					}
					o := js.NewObject()
					if f := js.Obj(fetched); f != nil {
						o = f.Clone()
					}
					o.Set("data", kept)
					res = o
				}
				return c.Out.Emit(res, func() []string {
					rows := js.List(js.Get(res, "data"))
					if len(rows) == 0 {
						return lines("No goals.")
					}
					ls := []string{}
					for _, g := range rows {
						ls = append(ls, goalLine(g))
					}
					if !hasInitiative {
						if f := output.PageFooter(len(rows), js.Get(res, "meta"), "Next page: not supported here — narrow with --status/--mine."); f != "" {
							ls = append(ls, f)
						}
					}
					return ls
				})
			},
		},
		{
			Path:     []string{"goals", "get"},
			Summary:  "Show a goal with its key results and latest check-in",
			Args:     "<goal-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc goals get <goal-id>"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.GetObjective(c.Args[0])
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					g := js.Get(res, "data")
					ls := []string{"# " + js.Str(js.Get(g, "title")), goalLine(g) + " · autonomy " + js.Str(js.Or(js.Get(g, "autonomy"), "-"))}
					if d := js.Get(g, "description"); js.Truthy(d) {
						ls = append(ls, "", output.TrimEnd(js.Str(d)))
					}
					krs := js.List(js.Get(g, "key_results"))
					ls = append(ls, "", fmt.Sprintf("## Key results (%d)", len(krs)))
					for _, kr := range krs {
						unit := ""
						if u := js.Get(kr, "unit"); js.Truthy(u) {
							unit = " " + js.Str(u)
						}
						ls = append(ls, fmt.Sprintf("- %s  %s  %s/%s%s  (score %s)", js.Str(js.Get(kr, "id")), js.Str(js.Get(kr, "title")),
							js.Str(js.Get(kr, "current_value")), js.Str(js.Get(kr, "target_value")), unit, js.Str(js.Get(kr, "score"))))
					}
					if ci := js.Get(g, "latest_checkin"); js.Truthy(ci) {
						submitted := ""
						if s := js.Get(ci, "submitted_at"); js.Truthy(s) {
							submitted = " · submitted " + output.FmtTime(s)
						}
						ls = append(ls, "", output.TrimEnd(fmt.Sprintf("Latest check-in: %s %s %s · week %s%s",
							js.S(js.Get(ci, "kind")), js.S(js.Get(ci, "status")), js.S(js.Get(ci, "confidence")),
							js.Str(js.Or(js.Get(ci, "week_start"), "-")), submitted)))
					}
					return ls
				})
			},
		},
		{
			Path:    []string{"goals", "create"},
			Summary: "Create a goal (owner, agent and initiative are required)",
			Options: opts(
				[]OptSpec{{Name: "title", Value: "<text>", Desc: "Title (required)"}},
				goalFlags,
				[]OptSpec{{Name: "kr", Multiple: true, Value: `<"Title|target|unit">`, Desc: "Key result (repeatable)"}},
			),
			Examples: []string{`gc goals create --title "Grow the newsletter" --initiative <initiative-id> --owner <owner-id> --agent <agent-id> --kr "Subscribers|5000|subs"`},
			Run: func(c *Ctx) error {
				title, err := c.requireStr("title")
				if err != nil {
					return err
				}
				desc, err := c.textInput("description", "description-file")
				if err != nil {
					return err
				}
				initiative, err := c.requireStr("initiative")
				if err != nil {
					return err
				}
				owner, err := c.requireStr("owner")
				if err != nil {
					return err
				}
				agent, err := c.requireStr("agent")
				if err != nil {
					return err
				}
				var krs any = js.Undefined
				if specs := c.list("kr"); len(specs) > 0 {
					list := []any{}
					for _, s := range specs {
						kr, err := parseKr(s)
						if err != nil {
							return err
						}
						list = append(list, kr)
					}
					krs = list
				}
				body := js.O("title", title, "description", desc, "initiative_id", initiative, "owner_member_id", owner,
					"agent_member_id", agent, "autonomy", c.sv("autonomy"), "key_results", krs)
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.CreateObjective(body)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("created  " + goalLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:    []string{"goals", "update"},
			Summary: "Update a goal. As the goal's own agent: only with your human's explicit approval",
			Args:    "<goal-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: opts([]OptSpec{
				{Name: "title", Value: "<text>", Desc: "New title"},
				{Name: "status", Value: "<s>", Desc: "active | achieved | missed | archived"},
			}, goalFlags),
			Examples: []string{"gc goals update <goal-id> --status achieved"},
			Run: func(c *Ctx) error {
				desc, err := c.textInput("description", "description-file")
				if err != nil {
					return err
				}
				patch := js.O("title", c.sv("title"), "description", desc, "status", c.sv("status"), "initiative_id", c.sv("initiative"),
					"owner_member_id", c.sv("owner"), "agent_member_id", c.sv("agent"), "autonomy", c.sv("autonomy"))
				if patch.Len() == 0 {
					return clierr.Usage("Nothing to update — pass at least one flag.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.UpdateObjective(c.Args[0], patch)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("updated  " + goalLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:    []string{"goals", "kr-update"},
			Summary: "Record key result progress (--current-value) or a manual score; title/target/unit need approval",
			Args:    "<goal-id> <kr-id>",
			MinArgs: 2,
			MaxArgs: 2,
			Options: []OptSpec{
				{Name: "current-value", Value: "<n>", Desc: "Current value (score is computed)"},
				{Name: "current", Value: "<n>", Desc: "Alias of --current-value"},
				{Name: "score", Value: "<0..1>", Desc: "Manual score override"},
				{Name: "title", Value: "<text>", Desc: "New title (with approval)"},
				{Name: "target", Value: "<n>", Desc: "New target (with approval)"},
				{Name: "unit", Value: "<unit>", Desc: "New unit (with approval)"},
			},
			Examples: []string{"gc goals kr-update <goal-id> <kr-id> --current-value 3200"},
			Run: func(c *Ctx) error {
				current, err := num(js.Or(c.sv("current-value"), c.sv("current")), "current-value", false)
				if err != nil {
					return err
				}
				score, err := num(c.sv("score"), "score", false)
				if err != nil {
					return err
				}
				target, err := num(c.sv("target"), "target", false)
				if err != nil {
					return err
				}
				patch := js.O("current_value", current, "score", score, "title", c.sv("title"), "target_value", target, "unit", c.sv("unit"))
				if patch.Len() == 0 {
					return clierr.Usage("Nothing to update — pass --current-value, --score, --title, --target or --unit.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.UpdateKeyResult(c.Args[0], c.Args[1], patch)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("updated key result " + c.Args[1]) })
			},
		},
		{
			Path:    []string{"goals", "checkin-submit"},
			Summary: "Submit a weekly/kickoff goal check-in (JSON body, see the item's playbook)",
			Args:    "<goal-id> <checkin-id>",
			MinArgs: 2,
			MaxArgs: 2,
			Options: []OptSpec{
				{Name: "body-file", Value: "<path|->", Desc: "JSON body from a file, or stdin with -"},
				{Name: "data", Value: "<json>", Desc: "JSON body inline"},
			},
			Details: strings.Join([]string{
				"Body: { confidence: on_track|at_risk|off_track, summary, research (required),",
				"  recommendation?: continue|adjust|rethink (required when the item has signals), recommendation_reason?,",
				"  kr_updates?: [{kr_id, current_value, note?}], kr_proposals?: [...], effects?: [{task_id, effect, note?}],",
				"  tasks?: [{title, hypothesis, description?, key_result_id?, priority?}] }",
				"Details: gc guide goals",
			}, "\n"),
			Examples: []string{"gc goals checkin-submit <goal-id> <checkin-id> --body-file - <<'EOF'\n  {\"confidence\":\"on_track\",\"summary\":\"…\",\"research\":\"…\"}\n  EOF"},
			Run: func(c *Ctx) error {
				body, err := c.jsonInput("data", "body-file")
				if err != nil {
					return err
				}
				if js.Obj(body) == nil {
					return clierr.Usage("The check-in body must be a JSON object.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.SubmitGoalCheckin(c.Args[0], c.Args[1], body)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("submitted check-in " + c.Args[1]) })
			},
		},
	}
}
