package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

const openStatuses = "backlog,scheduled,todo,in_progress,blocked,review"

func workflowRule(workflow any) string {
	if workflow == "scrum" {
		return "Workflow: scrum — finish your tasks with status review (a human sets done). backlog = parked: never start it."
	}
	return "Workflow: kanban — finish your tasks with status done. backlog = parked: never start it."
}

func featureList(f any) []string {
	out := []string{}
	switch x := f.(type) {
	case []any:
		for _, v := range x {
			out = append(out, js.Str(v))
		}
	case *js.Object:
		for _, k := range x.Keys() {
			if v, _ := x.Get(k); v == true {
				out = append(out, k)
			}
		}
	}
	return out
}

func contextCommands() []*Command {
	return []*Command{
		{
			Path:     []string{"context"},
			Summary:  "Who am I, which workspace, and my open assigned tasks — run this first",
			Examples: []string{"gc context", "gc context --field me.tenant.workflow"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := parallel(
					client.GetMe,
					func() (any, error) {
						return client.ListTasks(js.O("assigned_to", "me", "status", openStatuses, "limit", 100, "fields", "summary"))
					},
				)
				if err != nil {
					return err
				}
				me, tasks := res[0], res[1]
				rows := js.List(js.Get(tasks, "data"))
				if rows == nil {
					rows = []any{}
				}
				result := js.O("me", js.Get(me, "data"), "assigned_tasks", js.Or(js.Get(tasks, "data"), []any{}), "meta", js.Get(tasks, "meta"))
				return c.Out.Emit(result, func() []string {
					u := js.Or(js.Get(me, "data", "user"), js.NewObject())
					t := js.Or(js.Get(me, "data", "tenant"), js.NewObject())
					features := featureList(js.Get(t, "features"))
					profile := ""
					if a, err := c.Auth(); err == nil {
						profile = a.Profile
					}
					kind := "human"
					if js.Truthy(js.Get(u, "is_agent")) {
						kind = "agent"
					}
					first := fmt.Sprintf("%s (%s, %s) in workspace \"%s\"", js.Str(js.Or(js.Get(u, "display_name"), "?")), kind, js.Str(js.Or(js.Get(u, "role"), "?")), js.Str(js.Or(js.Get(t, "name"), "?")))
					if profile != "" {
						first += " · profile " + profile
					}
					feat := ""
					if len(features) > 0 {
						feat = "Features: " + strings.Join(features, ", ")
					}
					of := ""
					if total, ok := js.AsNumber(js.Get(tasks, "meta", "total")); ok && total > float64(len(rows)) {
						of = " of " + js.NumberString(total)
					}
					ls := []string{first, workflowRule(js.Get(t, "workflow")), feat, "", fmt.Sprintf("Open tasks assigned to me (%d%s):", len(rows), of)}
					if len(rows) == 0 {
						ls = append(ls, "  (none)")
					}
					for _, r := range rows {
						ls = append(ls, "  "+output.TaskLine(r))
					}
					ls = append(ls, "", "Next: gc changes --since 1h · gc tasks get <id> · gc guide")
					// drop an empty line that is first or follows another empty one
					kept := []string{}
					for i, l := range ls {
						if l != "" || (i > 0 && ls[i-1] != "") {
							kept = append(kept, l)
						}
					}
					return kept
				})
			},
		},
		{
			Path:    []string{"changes"},
			Summary: "What changed for me since a point in time (comments, tasks, docs, datasheets, goal check-ins)",
			Options: []OptSpec{
				{Name: "since", Value: "<iso|15m|2h|1d>", Desc: "Start of the window (default: the cursor file, else 1h)"},
				{Name: "cursor-file", Value: "<path>", Desc: `Read "since" from this file and write the new cursor back after a successful call`},
			},
			Details: strings.Join([]string{
				"There is no implicit stored cursor: every poller owns its own --cursor-file, or passes --since.",
				"Items marked [principal] concern the human you assist — do not start them, tell them in the session.",
			}, "\n"),
			Examples: []string{"gc changes --since 2h", "gc changes --cursor-file .gc-cursor", "gc changes --since 2026-10-02T06:00:00Z --json"},
			Run:      runChanges,
		},
		{
			Path:    []string{"members"},
			Summary: "People and agents in this workspace — the ids --assign and @-mentions need",
			Options: []OptSpec{
				{Name: "agents", Bool: true, Desc: "Only agents"},
				{Name: "humans", Bool: true, Desc: "Only humans"},
			},
			Examples: []string{"gc members", "gc members --humans"},
			Run: func(c *Ctx) error {
				q := ""
				if c.bool("agents") {
					q = "?is_agent=true"
				} else if c.bool("humans") {
					q = "?is_agent=false"
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.Get("/members" + q)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					rows := js.List(js.Get(res, "data"))
					if len(rows) == 0 {
						return lines("No members.")
					}
					ls := []string{}
					for _, m := range rows {
						role := js.Str(js.Get(m, "role"))
						if js.Truthy(js.Get(m, "is_agent")) {
							role = "agent"
						}
						ls = append(ls, fmt.Sprintf("%s  %s  (%s)", js.Str(js.Get(m, "id")), js.Str(js.Get(m, "display_name")), role))
					}
					return ls
				})
			},
		},
	}
}

func runChanges(c *Ctx) error {
	cursorFile, hasCursor := c.str("cursor-file")
	sinceFlag, hasSince := c.str("since")
	var since string
	var err error
	switch {
	case hasSince:
		since, err = parseSince(sinceFlag, time.Now())
		if err != nil {
			return err
		}
	case hasCursor && fileExists(cursorFile):
		b, rerr := os.ReadFile(cursorFile)
		if rerr != nil {
			return clierr.New(fmt.Sprintf("Cannot read %s: %s", cursorFile, rerr))
		}
		raw := strings.TrimSpace(string(b))
		since, err = parseSince(raw, time.Now())
		if err != nil {
			return clierr.New(fmt.Sprintf("Cursor file %s does not hold a timestamp (\"%s\").", cursorFile, output.OneLine(raw, 40)), "Pass --since to reset it.")
		}
	default:
		since, _ = parseSince("1h", time.Now())
		if hasCursor {
			c.Out.Err(fmt.Sprintf("note: %s does not exist yet — using the last hour.", cursorFile))
		}
	}

	client, err := c.Client()
	if err != nil {
		return err
	}
	res, err := client.GetChanges(since)
	if err != nil {
		return err
	}

	cursor := ""
	if hasCursor {
		cursor = serverCursor(res)
		if cursor == "" {
			return clierr.New("The server returned no cursor (meta.cursor / meta.checked_at); the cursor file was not changed.")
		}
	}

	// Print first, advance the cursor last: a failing --field or a closed
	// pipe must not move the cursor past items nobody saw.
	if err := c.Out.Emit(res, func() []string { return formatChanges(res, since, cursor) }); err != nil {
		return err
	}
	if hasCursor {
		if err := os.MkdirAll(filepath.Dir(cursorFile), 0o777); err != nil {
			return err
		}
		return config.AtomicWrite(cursorFile, cursor+"\n", 0o600)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// serverCursor is the next `since`, always from the server's clock:
// `meta.cursor` is taken BEFORE the server's queries run (rows committed during
// the request land in the next window); `checked_at` is the fallback for older
// servers. Never the local clock — it drifts against the database's.
func serverCursor(res any) string {
	cv := js.Or(js.Get(res, "meta", "cursor"), js.Get(res, "meta", "checked_at"))
	s, ok := cv.(string)
	if !ok {
		return ""
	}
	if _, ok := js.ParseDate(s); !ok {
		return ""
	}
	return s
}

func formatChanges(res any, since, cursor string) []string {
	d := js.Or(js.Get(res, "data"), js.NewObject())
	head := "Changes since " + output.FmtTime(since)
	if cursor != "" {
		head += " (cursor saved: " + cursor + ")"
	}
	ls := []string{head}
	count := 0
	principal := false
	if o := js.Obj(d); o != nil {
		for _, k := range o.Keys() {
			v, _ := o.Get(k)
			for _, x := range js.List(v) {
				if js.Get(x, "for") == "principal" {
					principal = true
				}
			}
		}
	}
	tag := func(x any) string {
		if js.Get(x, "for") == "principal" {
			return "[principal] "
		}
		return ""
	}
	section := func(title string, key string, fmtItem func(x any, i int) []string) {
		items, ok := js.Arr(js.Get(d, key))
		if !ok || len(items) == 0 {
			return
		}
		count += len(items)
		ls = append(ls, "", fmt.Sprintf("%s (%d):", title, len(items)))
		for i, x := range items {
			for _, l := range fmtItem(x, i) {
				ls = append(ls, "  "+l)
			}
		}
	}
	g := js.Get
	section("Task comments", "task_comments", func(x any, _ int) []string {
		return lines(
			fmt.Sprintf("%stask %s \"%s\" · %s", tag(x), js.Str(g(x, "task_id")), js.S(g(x, "task_title")), output.FmtTime(g(x, "created_at"))),
			"  "+output.OneLine(g(x, "content"), 300),
		)
	})
	section("Tasks created", "tasks_created", func(x any, _ int) []string { return lines(output.TaskLine(x)) })
	section("Tasks updated", "tasks_updated", func(x any, _ int) []string {
		return lines(output.TaskLine(x) + "  · " + output.FmtTime(g(x, "updated_at")))
	})
	section("Doc comments", "doc_comments", func(x any, _ int) []string {
		return lines(fmt.Sprintf("%sdoc %s · %s", tag(x), js.Str(g(x, "doc_id")), output.FmtTime(g(x, "created_at"))), "  "+output.OneLine(g(x, "body"), 300))
	})
	section("Docs updated", "docs_updated", func(x any, _ int) []string {
		return lines(fmt.Sprintf("%s%s  %s · %s", tag(x), js.Str(g(x, "id")), js.Str(g(x, "title")), output.FmtTime(g(x, "updated_at"))))
	})
	rowRef := func(x any) string { return js.S(js.Or(g(x, "id"), g(x, "row_id"), "")) }
	section("Datasheet rows created", "table_rows_created", func(x any, _ int) []string {
		return lines(fmt.Sprintf("%sdatasheet %s \"%s\" row %s", tag(x), js.Str(g(x, "table_id")), js.S(g(x, "table_name")), rowRef(x)))
	})
	section("Datasheet rows updated", "table_rows_updated", func(x any, _ int) []string {
		return lines(fmt.Sprintf("%sdatasheet %s \"%s\" row %s", tag(x), js.Str(g(x, "table_id")), js.S(g(x, "table_name")), rowRef(x)))
	})
	section("Datasheet comments", "table_comments", func(x any, _ int) []string {
		return lines(fmt.Sprintf("%sdatasheet %s \"%s\" · %s", tag(x), js.Str(g(x, "table_id")), js.S(g(x, "table_name")), output.FmtTime(g(x, "created_at"))), "  "+output.OneLine(g(x, "body"), 300))
	})
	section("Goal check-ins", "goal_checkins", func(x any, i int) []string {
		goalTitle := js.S(g(x, "goal", "title"))
		goalID := js.S(g(x, "goal", "id"))
		if g(x, "kind") == "reply" {
			return lines(
				fmt.Sprintf("reply on check-in %s for goal \"%s\" (%s)", js.Str(js.Or(g(x, "checkin_id"), g(x, "id"))), goalTitle, goalID),
				"  "+output.OneLine(g(x, "owner_reply"), 300),
			)
		}
		sig := ""
		if signals := js.List(g(x, "signals")); len(signals) > 0 {
			codes := []string{}
			for _, s := range signals {
				codes = append(codes, js.Str(js.Or(g(s, "code"), s)))
			}
			sig = " · signals: " + strings.Join(codes, ", ")
		}
		submitGoal := goalID
		if js.Nullish(g(x, "goal", "id")) {
			submitGoal = "<goal-id>"
		}
		return lines(
			fmt.Sprintf("%s check-in %s for goal \"%s\" (%s)%s", js.Str(g(x, "kind")), js.Str(g(x, "id")), goalTitle, goalID, sig),
			fmt.Sprintf("  read: gc changes --since %s --field data.goal_checkins.%d   (follow its playbook)", since, i),
			fmt.Sprintf("  submit: gc goals checkin-submit %s %s --body-file -", submitGoal, js.Str(g(x, "id"))),
		)
	})

	if count == 0 {
		ls = append(ls, "Nothing new.")
	}
	if principal {
		ls = append(ls, "", "[principal] = concerns the human you assist: do NOT start it — tell them about it in this session.")
	}
	return ls
}
