package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

const statuses = "backlog | scheduled | todo | in_progress | blocked | review | done"

var taskFieldFlags = []OptSpec{
	{Name: "description", Value: "<md>", Desc: "Markdown description"},
	{Name: "description-file", Value: "<path|->", Desc: "Read the description from a file, or stdin with -"},
	{Name: "status", Value: "<status>", Desc: statuses},
	{Name: "priority", Value: "<p>", Desc: "low | medium | high | critical"},
	{Name: "assign", Value: "<me|member-id>", Desc: `Assignee: "me" or a member id`},
	{Name: "initiative", Value: "<id>", Desc: "Initiative id (omit = personal task)"},
	{Name: "due", Value: "<YYYY-MM-DD>", Desc: "Due date"},
	{Name: "scheduled", Value: "<YYYY-MM-DD>", Desc: "Park it until then: status scheduled + that date; it returns as todo in gc changes (scheduled_due) at 06:00 Vienna"},
	{Name: "story-points", Value: "<n>", Desc: "Estimate 0–999 (scrum workspaces)"},
}

func withTitle(desc string) []OptSpec {
	return append([]OptSpec{{Name: "title", Value: "<text>", Desc: desc}}, taskFieldFlags...)
}

func taskPayload(c *Ctx, update bool) (*js.Object, error) {
	nul := func(v any) any {
		if update {
			return nullable(v)
		}
		return v
	}
	desc, err := c.textInput("description", "description-file")
	if err != nil {
		return nil, err
	}
	sp, err := num(c.sv("story-points"), "story-points", update)
	if err != nil {
		return nil, err
	}
	status, scheduled, err := scheduledFields(c, update)
	if err != nil {
		return nil, err
	}
	return js.O(
		"title", c.sv("title"),
		"description", desc,
		"status", status,
		"priority", c.sv("priority"),
		"assigned_to", nul(c.sv("assign")),
		"initiative_id", nul(c.sv("initiative")),
		"due_date", nul(c.sv("due")),
		"scheduled_date", scheduled,
		"story_points", sp,
	), nil
}

// scheduledFields turns --scheduled into status + scheduled_date. A date means
// "park it until then", so it sets status scheduled too — a scheduled_date on a
// todo task wakes nothing. "none" (update only) clears the date and leaves the
// status alone: if the task stays scheduled, the server warns
// scheduled_without_date and the warning is printed.
func scheduledFields(c *Ctx, update bool) (status, scheduled any, err error) {
	status, scheduled = c.sv("status"), js.Undefined
	v, ok := c.sv("scheduled").(string)
	if !ok {
		return status, scheduled, nil
	}
	if v == "none" || v == "null" {
		if !update {
			return nil, nil, clierr.Usage("--scheduled none only clears a date on gc tasks update.")
		}
		return status, nil, nil
	}
	if _, perr := time.Parse("2006-01-02", v); perr != nil {
		return nil, nil, clierr.Usage(fmt.Sprintf("--scheduled must be a date YYYY-MM-DD (or none), got \"%s\".", v))
	}
	if s, ok := status.(string); ok && s != "scheduled" {
		return nil, nil, clierr.Usage(fmt.Sprintf("--scheduled sets status scheduled; it can't be combined with --status %s.", s))
	}
	return "scheduled", v, nil
}

func listLines(res any, line func(any) string, empty string) []string {
	rows := js.List(js.Get(res, "data"))
	if len(rows) == 0 {
		return lines(empty)
	}
	ls := []string{}
	for _, r := range rows {
		ls = append(ls, line(r))
	}
	if f := output.PageFooter(len(rows), js.Get(res, "meta"), "Next page: --offset {next}"); f != "" {
		ls = append(ls, f)
	}
	return ls
}

func taskCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"tasks", "list"},
			Summary: "List tasks (newest first). Your queue: --assigned-to me",
			Options: []OptSpec{
				{Name: "status", Value: "<a,b>", Desc: "Comma-separated: " + statuses},
				{Name: "assigned-to", Value: "<me|id>", Desc: `"me" or a member id`},
				{Name: "priority", Value: "<p>", Desc: "low | medium | high | critical"},
				{Name: "initiative", Value: "<id>", Desc: "Only this initiative"},
				{Name: "q", Value: "<text>", Desc: "Title/description contains"},
				{Name: "sort", Value: "<key>", Desc: "created_at (default) | completed_at | rank (backlog order)"},
				{Name: "estimated", Value: "<true|false>", Desc: "false = tasks without story points"},
				{Name: "limit", Value: "<n>", Desc: "Max rows (default 50, server max 100)"},
				{Name: "offset", Value: "<n>", Desc: "Skip rows (paging)"},
			},
			Examples: []string{
				"gc tasks list --assigned-to me --status todo,in_progress",
				"gc tasks list --status backlog --sort rank --limit 1",
			},
			Run: func(c *Ctx) error {
				var fields any = "summary" // the board's lean rows (no description) — text output never shows one
				if c.Out.Raw() {
					fields = js.Undefined
				}
				params := js.O(
					"status", c.sv("status"),
					"assigned_to", c.sv("assigned-to"),
					"priority", c.sv("priority"),
					"initiative_id", c.sv("initiative"),
					"q", c.sv("q"),
					"sort", c.sv("sort"),
					"estimated", c.sv("estimated"),
					"limit", js.Or(c.sv("limit"), "50"),
					"offset", c.sv("offset"),
					"fields", fields,
				)
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.ListTasks(params)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return listLines(res, output.TaskLine, "No tasks.") })
			},
		},
		{
			Path:    []string{"tasks", "get"},
			Summary: "Show a task: details, description, attachments and comments",
			Args:    "<task-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "comments", Value: "<all|new|none>", Desc: "Which comments to print (default all; new needs --since)"},
				{Name: "since", Value: "<iso|15m|2h|1d>", Desc: "With --comments new (implied): only comments after this"},
			},
			Examples: []string{"gc tasks get <task-id>", "gc tasks get <task-id> --since 2h", "gc tasks get <task-id> --field description"},
			Run:      runTaskGet,
		},
		{
			Path:    []string{"tasks", "create"},
			Summary: "Create a task",
			Options: withTitle("Title (required)"),
			Examples: []string{
				`gc tasks create --title "Fix login redirect" --priority high --assign me`,
				`gc tasks create --title "Write report" --description-file - < notes.md`,
				`gc tasks create --title "Follow up on the offer" --scheduled 2026-11-17 --assign me`,
			},
			Run: func(c *Ctx) error {
				if _, err := c.requireStr("title"); err != nil {
					return err
				}
				payload, err := taskPayload(c, false)
				if err != nil {
					return err
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.CreateTask(payload)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("created  " + output.TaskLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:     []string{"tasks", "update"},
			Summary:  `Update a task (only the flags you pass change; "none" clears assign/initiative/due/scheduled/story-points)`,
			Args:     "<task-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Options:  withTitle("New title"),
			Details:  "In a scrum workspace (gc context) finish with --status review, never done.",
			Examples: []string{"gc tasks update <task-id> --status in_progress", "gc tasks update <task-id> --status review", "gc tasks update <task-id> --assign none", "gc tasks update <task-id> --scheduled 2026-11-17"},
			Run: func(c *Ctx) error {
				payload, err := taskPayload(c, true)
				if err != nil {
					return err
				}
				if payload.Len() == 0 {
					return clierr.Usage("Nothing to update — pass at least one flag.", "See gc tasks update --help.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.UpdateTask(c.Args[0], payload)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("updated  " + output.TaskLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:    []string{"tasks", "ask"},
			Summary: "Ask a human structured multiple-choice questions on a task (the answer arrives as a comment)",
			Args:    "<task-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "body-file", Value: "<path|->", Desc: `The ask as JSON: {"questions":[…], "content"?, "to"?} — a file, or stdin with -`},
				{Name: "to", Value: "<member-id>", Desc: "Ask this human (default: your responsible human)"},
				{Name: "content", Value: "<md>", Desc: "Intro / context shown above the questions"},
				{Name: "content-file", Value: "<path|->", Desc: "Read the intro from a file, or stdin with -"},
			},
			Details: strings.Join([]string{
				"Instead of a question comment: the human answers on their Desk with one click, and the answer is",
				"posted back on the task as an ordinary comment — watch for it with gc changes.",
				`JSON: {"questions":[{"prompt":"…","header"?:"…","options":[{"label":"…","description"?:"…"}],`,
				`"multi"?:false,"allow_other"?:true}], "content"?:"…", "to"?:"<member-id>"}`,
				"1–10 questions with 1–9 options each; the server validates the details. --to and --content(-file)",
				"override the JSON's to/content. HTTP 404 = no such task, or the Desk is off in this workspace.",
			}, "\n"),
			Examples: []string{
				"gc tasks ask <task-id> --body-file - <<'EOF'\n  {\"content\": \"Two ways to ship this:\", \"questions\": [{\"prompt\": \"Which one?\",\n   \"options\": [{\"label\": \"Today, without tests\"}, {\"label\": \"Friday, with tests\"}]}]}\n  EOF",
				"gc tasks ask <task-id> --body-file ask.json --to <member-id>",
			},
			Run: runTaskAsk,
		},
		{
			Path:    []string{"comment"},
			Summary: "Post a Markdown comment on a task (progress, results, blockers)",
			Args:    "<task-id> [<body> | -]",
			MinArgs: 1,
			MaxArgs: 2,
			Options: []OptSpec{{Name: "body-file", Value: "<path|->", Desc: "Read the Markdown body from a file, or stdin with -"}},
			Examples: []string{
				`gc comment <task-id> "Started — reproducing the bug now."`,
				"gc comment <task-id> --body-file - <<'EOF'\n  ## Done\n  - PR: https://github.com/…\n  EOF",
			},
			Run: func(c *Ctx) error {
				body, err := c.bodyInput(c.arg(1))
				if err != nil {
					return err
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.CreateComment(c.Args[0], body)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					return lines(fmt.Sprintf("commented on %s (%s)", c.Args[0], js.Str(js.Or(js.Get(res, "data", "id"), "ok"))))
				})
			},
		},
		{
			Path:     []string{"attach"},
			Summary:  "Attach a local file to a task (any type, up to 50 MB)",
			Args:     "<task-id> <path>",
			MinArgs:  2,
			MaxArgs:  2,
			Options:  []OptSpec{{Name: "name", Value: "<file-name>", Desc: "Store it under this name"}},
			Examples: []string{"gc attach <task-id> ./screenshot.png", "gc attach <task-id> build.log --name build-2026-10-02.log"},
			Run: func(c *Ctx) error {
				taskID, path := c.Args[0], c.Args[1]
				if !fileExists(path) {
					return clierr.New("No such file: " + path)
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				name, _ := c.str("name")
				res, err := client.UploadAttachment(taskID, path, name)
				if err != nil {
					return err
				}
				a := js.Or(js.Get(res, "data"), js.NewObject())
				return c.Out.Emit(res, func() []string {
					return lines(fmt.Sprintf("attached %s (%s) to %s", js.Str(js.Or(js.Get(a, "file_name"), filepath.Base(path))), output.HumanSize(js.Get(a, "file_size")), taskID))
				})
			},
		},
	}
}

func runTaskGet(c *Ctx) error {
	id := c.Args[0]
	sinceRaw, hasSince := c.str("since")
	mode, ok := c.str("comments")
	if !ok {
		mode = "all"
		if hasSince {
			mode = "new"
		}
	}
	if mode != "all" && mode != "new" && mode != "none" {
		return clierr.Usage("--comments must be all, new or none.")
	}
	if mode == "new" && !hasSince {
		return clierr.Usage("--comments new needs --since <iso|15m|2h|1d>.")
	}
	since := ""
	if hasSince {
		s, err := parseSince(sinceRaw, time.Now())
		if err != nil {
			return err
		}
		since = s
	}
	client, err := c.Client()
	if err != nil {
		return err
	}
	res, err := parallel(
		func() (any, error) { return client.GetTask(id) },
		func() (any, error) {
			if mode == "none" {
				return js.O("data", []any{}), nil
			}
			return client.ListComments(id)
		},
		func() (any, error) { return client.ListAttachments(id) },
	)
	if err != nil {
		return err
	}
	taskRes, commentsRes, attachmentsRes := res[0], res[1], res[2]
	comments := js.List(js.Get(commentsRes, "data"))
	total := len(comments)
	if since != "" {
		st, _ := js.ParseDate(since)
		kept := []any{}
		for _, cm := range comments {
			if t, ok := js.ParseDate(js.S(js.Get(cm, "created_at"))); ok && t.After(st) {
				kept = append(kept, cm)
			}
		}
		comments = kept
	}
	if comments == nil {
		comments = []any{}
	}
	attachments := js.List(js.Get(attachmentsRes, "data"))
	if attachments == nil {
		attachments = []any{}
	}
	merged := js.NewObject()
	if o := js.Obj(taskRes); o != nil {
		merged = o.Clone()
	}
	data := js.NewObject()
	if o := js.Obj(js.Get(taskRes, "data")); o != nil {
		data = o.Clone()
	}
	data.Set("comments", comments)
	data.Set("attachments", attachments)
	merged.Set("data", data)

	return c.Out.Emit(merged, func() []string {
		t := data
		g := func(k ...string) any { return js.Get(t, k...) }
		meta := []string{js.JoinStr(g("status")), js.JoinStr(g("priority"))}
		if who := output.Name(g("assignee")); js.Truthy(who) {
			meta = append(meta, "@"+js.Str(who))
		} else {
			meta = append(meta, "unassigned")
		}
		if n := g("initiative", "name"); js.Truthy(n) {
			meta = append(meta, "#"+js.Str(n))
		} else {
			meta = append(meta, "personal")
		}
		if d := g("due_date"); js.Truthy(d) {
			meta = append(meta, "due "+js.Str(d))
		}
		if d := g("scheduled_date"); js.Truthy(d) && g("status") == "scheduled" {
			meta = append(meta, "scheduled for "+js.Str(d))
		}
		if sp := g("story_points"); !js.Nullish(sp) {
			meta = append(meta, js.Str(sp)+"sp")
		}
		meta = append(meta, "updated "+output.FmtTime(g("updated_at")))
		ls := []string{"# " + js.Str(g("title")), js.Str(g("id")) + " · " + output.Join(" · ", meta...), ""}
		if d := g("description"); js.Truthy(d) {
			ls = append(ls, output.TrimEnd(js.Str(d)))
		} else {
			ls = append(ls, "(no description)")
		}
		if len(attachments) > 0 {
			ls = append(ls, "", fmt.Sprintf("## Attachments (%d)", len(attachments)))
			for _, a := range attachments {
				ls = append(ls, output.TrimEnd(fmt.Sprintf("- %s (%s) %s", js.Str(js.Get(a, "file_name")), output.HumanSize(js.Get(a, "file_size")), js.S(js.Get(a, "file_url")))))
			}
		}
		if mode != "none" {
			label := fmt.Sprint(total)
			if since != "" {
				label = fmt.Sprintf("%d new since %s, %d total", len(comments), output.FmtTime(since), total)
			}
			ls = append(ls, "", "## Comments ("+label+")")
			for _, cm := range comments {
				ls = append(ls, output.CommentBlock(cm))
			}
		}
		return ls
	})
}

func runTaskAsk(c *Ctx) error {
	if err := c.oneStdin("body-file", "content-file"); err != nil {
		return err
	}
	file, ok := c.str("body-file")
	if !ok {
		return clierr.Usage(`Missing --body-file <path|-> with the ask as JSON ({"questions":[…]}).`, "See gc tasks ask --help.")
	}
	raw, err := c.readSource(file)
	if err != nil {
		return err
	}
	parsed, err := parseJSON(raw, "--body-file")
	if err != nil {
		return err
	}
	body := js.Obj(parsed)
	if body == nil {
		return clierr.Usage("--body-file must hold a JSON object with a questions array.")
	}
	body = body.Clone()
	if qs, ok := js.Arr(js.Get(body, "questions")); !ok || len(qs) == 0 {
		return clierr.Usage("--body-file needs a non-empty \"questions\" array.", "See gc tasks ask --help.")
	}
	content, err := c.textInput("content", "content-file")
	if err != nil {
		return err
	}
	if content != js.Undefined {
		body.Set("content", content)
	}
	if to, ok := c.str("to"); ok {
		body.Set("to", to)
	}
	client, err := c.Client()
	if err != nil {
		return err
	}
	res, err := client.CreateAsk(c.Args[0], body)
	if err != nil {
		if strings.Contains(err.Error(), " 404:") {
			return clierr.New(err.Error(), "HTTP 404 = no such task, or the Desk (asks) is off in this workspace — ask in a comment instead.")
		}
		return err
	}
	return c.Out.Emit(res, func() []string {
		d := js.Get(res, "data")
		n := len(js.List(js.Or(js.Get(d, "questions"), js.Get(body, "questions"))))
		to := js.Str(js.Or(js.Get(d, "ask_to_member_id"), "your responsible human"))
		return lines(fmt.Sprintf("asked %s on %s (%d question(s) to %s) — the answer arrives as a comment on the task", js.Str(js.Or(js.Get(d, "id"), "ok")), c.Args[0], n, to))
	})
}
