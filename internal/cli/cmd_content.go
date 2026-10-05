package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

func initiativeOrPersonal(x any) string {
	if n := js.Get(x, "initiative", "name"); js.Truthy(n) {
		return "#" + js.Str(n)
	}
	return "personal"
}

func docLine(d any) string {
	archived := ""
	if js.Truthy(js.Get(d, "archived_at")) {
		archived = "(archived)"
	}
	return output.Join("  ", js.JoinStr(js.Get(d, "id")), output.FmtTime(js.Get(d, "updated_at")), js.JoinStr(js.Get(d, "title")), initiativeOrPersonal(d), archived)
}

func initiativeLine(i any) string {
	counts := ""
	if _, ok := js.AsNumber(js.Get(i, "task_count")); ok {
		counts = fmt.Sprintf("%s tasks, %s docs", js.Str(js.Get(i, "task_count")), js.Str(js.Or(js.Get(i, "doc_count"), 0.0)))
	}
	hidden := ""
	if js.Get(i, "content_visible") == false {
		hidden = "(contents not visible to you)"
	}
	archived := ""
	if js.Truthy(js.Get(i, "archived_at")) {
		archived = "(archived)"
	}
	return output.Join("  ", js.JoinStr(js.Get(i, "id")), output.Pad(js.Get(i, "visibility"), 7), js.JoinStr(js.Get(i, "name")), counts, hidden, archived)
}

func docCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"docs", "list"},
			Summary: "List documents",
			Options: []OptSpec{
				{Name: "initiative", Value: "<id>", Desc: "Only this initiative"},
				{Name: "q", Value: "<text>", Desc: "Title/content contains"},
				{Name: "archived", Bool: true, Desc: "Include archived docs"},
				{Name: "limit", Value: "<n>", Desc: "Max rows (default 50)"},
				{Name: "offset", Value: "<n>", Desc: "Skip rows (paging)"},
			},
			Examples: []string{"gc docs list", "gc docs list --initiative <initiative-id> --q roadmap"},
			Run: func(c *Ctx) error {
				var archived any = js.Undefined
				if c.bool("archived") {
					archived = "true"
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.ListDocs(js.O("initiative_id", c.sv("initiative"), "q", c.sv("q"), "archived", archived,
					"limit", js.Or(c.sv("limit"), "50"), "offset", c.sv("offset")))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return listLines(res, docLine, "No docs.") })
			},
		},
		{
			Path:     []string{"docs", "get"},
			Summary:  "Show a document (Markdown content)",
			Args:     "<doc-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc docs get <doc-id>", "gc docs get <doc-id> --field content > doc.md"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.GetDoc(c.Args[0])
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					d := js.Get(res, "data")
					by := js.Or(output.Name(js.Get(d, "updated_by")), output.Name(js.Get(d, "created_by")))
					meta := fmt.Sprintf("%s · %s · updated %s", js.Str(js.Get(d, "id")), initiativeOrPersonal(d), output.FmtTime(js.Get(d, "updated_at")))
					if js.Truthy(by) {
						meta += " by " + js.Str(by)
					}
					if js.Truthy(js.Get(d, "archived_at")) {
						meta += " · archived"
					}
					ls := []string{"# " + js.Str(js.Get(d, "title")), meta, "", output.TrimEnd(js.S(js.Get(d, "content")))}
					tasks := js.List(js.Or(js.Get(d, "linked_tasks"), js.Get(d, "tasks"), []any{}))
					if len(tasks) > 0 {
						ls = append(ls, "", fmt.Sprintf("## Linked tasks (%d)", len(tasks)))
						for _, t := range tasks {
							ls = append(ls, fmt.Sprintf("- %s  %s  %s", js.Str(js.Get(t, "id")), js.S(js.Get(t, "status")), js.Str(js.Get(t, "title"))))
						}
					}
					comments := js.List(js.Get(d, "comments"))
					if len(comments) > 0 {
						ls = append(ls, "", fmt.Sprintf("## Comments (%d)", len(comments)))
						for _, cm := range comments {
							ls = append(ls, output.CommentBlock(cm))
						}
					}
					return ls
				})
			},
		},
		{
			Path:    []string{"docs", "create"},
			Summary: "Create a Markdown document (no initiative = personal)",
			Options: []OptSpec{
				{Name: "title", Value: "<text>", Desc: "Title (required)"},
				{Name: "content", Value: "<md>", Desc: "Markdown content"},
				{Name: "content-file", Value: "<path|->", Desc: "Read the content from a file, or stdin with -"},
				{Name: "initiative", Value: "<id>", Desc: "Initiative id"},
				{Name: "slug", Value: "<slug>", Desc: "URL slug (default: from the title)"},
			},
			Examples: []string{`gc docs create --title "Release notes" --content-file notes.md --initiative <initiative-id>`},
			Run: func(c *Ctx) error {
				content, err := c.textInput("content", "content-file")
				if err != nil {
					return err
				}
				if content == js.Undefined {
					return clierr.Usage("Missing --content or --content-file.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				title, err := c.requireStr("title")
				if err != nil {
					return err
				}
				res, err := client.CreateDoc(js.O("title", title, "content", content, "initiative_id", c.sv("initiative"), "slug", c.sv("slug")))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("created  " + docLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:    []string{"docs", "update"},
			Summary: "Update a document (only what you pass changes)",
			Args:    "<doc-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "title", Value: "<text>", Desc: "New title"},
				{Name: "content", Value: "<md>", Desc: "New Markdown content (replaces it)"},
				{Name: "content-file", Value: "<path|->", Desc: "Read the new content from a file, or stdin with -"},
				{Name: "initiative", Value: "<id|none>", Desc: `Move to an initiative ("none" = personal)`},
			},
			Examples: []string{"gc docs update <doc-id> --content-file doc.md"},
			Run: func(c *Ctx) error {
				content, err := c.textInput("content", "content-file")
				if err != nil {
					return err
				}
				patch := js.O("title", c.sv("title"), "content", content, "initiative_id", nullable(c.sv("initiative")))
				if patch.Len() == 0 {
					return clierr.Usage("Nothing to update — pass --title, --content(-file) or --initiative.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.UpdateDoc(c.Args[0], patch)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("updated  " + docLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:     []string{"docs", "archive"},
			Summary:  "Archive (soft-delete) a document",
			Args:     "<doc-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc docs archive <doc-id>"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.ArchiveDoc(c.Args[0])
				if err != nil {
					return err
				}
				return c.Out.Emit(js.Or(res, js.O("archived", c.Args[0])), func() []string { return lines("archived " + c.Args[0]) })
			},
		},
		{
			Path:     []string{"docs", "comment"},
			Summary:  "Post a Markdown comment on a document",
			Args:     "<doc-id> [<body> | -]",
			MinArgs:  1,
			MaxArgs:  2,
			Options:  []OptSpec{{Name: "body-file", Value: "<path|->", Desc: "Read the body from a file, or stdin with -"}},
			Examples: []string{`gc docs comment <doc-id> "Updated section 2."`},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				body, err := c.bodyInput(c.arg(1))
				if err != nil {
					return err
				}
				res, err := client.CreateDocComment(c.Args[0], body)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					return lines(fmt.Sprintf("commented on doc %s (%s)", c.Args[0], js.Str(js.Or(js.Get(res, "data", "id"), "ok"))))
				})
			},
		},
	}
}

func initiativeCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"initiatives", "list"},
			Summary: "List initiatives (project containers) you can see",
			Options: []OptSpec{
				{Name: "counts", Bool: true, Desc: "Include task/doc counts"},
				{Name: "limit", Value: "<n>", Desc: "Max rows (default 100)"},
				{Name: "offset", Value: "<n>", Desc: "Skip rows (paging)"},
			},
			Examples: []string{"gc initiatives list", "gc initiatives list --counts"},
			Run: func(c *Ctx) error {
				var counts any = js.Undefined
				if c.bool("counts") {
					counts = "true"
				}
				q := js.O("limit", js.Or(c.sv("limit"), "100"), "offset", c.sv("offset"), "with_counts", counts)
				parts := []string{}
				for _, k := range q.Keys() {
					v, _ := q.Get(k)
					parts = append(parts, k+"="+queryEscape(js.Str(v)))
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.Get("/initiatives?" + strings.Join(parts, "&"))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return listLines(res, initiativeLine, "No initiatives.") })
			},
		},
		{
			Path:     []string{"initiatives", "get"},
			Summary:  "Show an initiative with its counts and summary",
			Args:     "<initiative-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc initiatives get <initiative-id>"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.GetInitiative(c.Args[0])
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					i := js.Get(res, "data")
					opt := func(prefix, key string) string {
						if v := js.Get(i, key); js.Truthy(v) {
							return "\n" + prefix + js.Str(v)
						}
						return ""
					}
					return keepNonEmpty([]string{
						"# " + js.Str(js.Get(i, "name")),
						initiativeLine(i),
						opt("", "description"),
						opt("Summary: ", "summary"),
						opt("Memory: ", "memory_summary"),
					})
				})
			},
		},
		{
			Path:     []string{"initiatives", "memory"},
			Summary:  "An initiative's agent memory (insights, key decisions, stats)",
			Args:     "<initiative-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc initiatives memory <initiative-id> --json"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.GetInitiativeMemory(c.Args[0])
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines(js.StringifyIndent(js.Or(js.Get(res, "data"), res))) })
			},
		},
	}
}

func searchCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"search"},
			Summary: "Full-text search across tasks, docs and comments",
			Args:    "<query>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "type", Value: "<t>", Desc: "tasks | docs | comments | all (default)"},
				{Name: "initiative", Value: "<id>", Desc: "Only this initiative"},
				{Name: "limit", Value: "<n>", Desc: "Max results per type (default 20)"},
			},
			Examples: []string{`gc search "login redirect"`, "gc search invoice --type docs"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.Search(js.O("q", c.Args[0], "type", c.sv("type"), "initiative_id", c.sv("initiative"), "limit", c.sv("limit")))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					d := js.Get(res, "data")
					ls := []string{}
					for _, t := range js.List(js.Get(d, "tasks")) {
						ls = append(ls, fmt.Sprintf("task     %s  %s %s", js.Str(js.Get(t, "id")), output.Pad(js.Get(t, "status"), 11), js.Str(js.Get(t, "title"))))
					}
					for _, x := range js.List(js.Get(d, "docs")) {
						ls = append(ls, fmt.Sprintf("doc      %s  %s", js.Str(js.Get(x, "id")), js.Str(js.Get(x, "title"))))
					}
					for _, cm := range js.List(js.Get(d, "comments")) {
						src := "task"
						if js.Get(cm, "source_type") == "doc_comment" {
							src = "doc"
						}
						ls = append(ls, fmt.Sprintf("comment  %s %s \"%s\" · %s: %s", src, js.Str(js.Get(cm, "source_id")), js.Str(js.Get(cm, "source_title")),
							js.Str(js.Or(output.Name(js.Get(cm, "author")), "?")), output.OneLine(js.Get(cm, "body"), 120)))
					}
					if len(ls) == 0 {
						return lines("No results.")
					}
					return ls
				})
			},
		},
		{
			Path:    []string{"semantic-search"},
			Summary: "Find tasks, docs and comments by meaning (needs OpenAI on the server)",
			Args:    "<query>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "types", Value: "<a,b>", Desc: "task, doc, task_comment, doc_comment, journal"},
				{Name: "limit", Value: "<n>", Desc: "1–50 (default 10)"},
			},
			Examples: []string{`gc semantic-search "why did we drop sub-tasks"`},
			Run: func(c *Ctx) error {
				var types any = js.Undefined
				if s, ok := c.str("types"); ok {
					t := []string{}
					for _, p := range strings.Split(s, ",") {
						if p = strings.TrimSpace(p); p != "" {
							t = append(t, p)
						}
					}
					types = t
				}
				limit, err := num(c.sv("limit"), "limit", false)
				if err != nil {
					return err
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.SemanticSearch(js.O("query", c.Args[0], "types", types, "limit", limit))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					hits := js.List(js.Get(res, "data", "hits"))
					if len(hits) == 0 {
						return lines(js.Str(js.Or(js.Get(res, "data", "note"), "No results.")))
					}
					ls := []string{}
					for _, h := range hits {
						sim := ""
						if f, ok := js.AsNumber(js.Get(h, "similarity")); ok {
							sim = strconv.FormatFloat(f, 'f', 2, 64)
						}
						snippet := ""
						if s := js.Get(h, "snippet"); js.Truthy(s) {
							snippet = "— " + output.OneLine(s, 120)
						}
						ls = append(ls, output.TrimEnd(fmt.Sprintf("%s %s  %s  %s %s", output.Pad(js.Get(h, "entity_type"), 12), js.Str(js.Get(h, "entity_id")), sim, js.S(js.Get(h, "title")), snippet)))
					}
					return ls
				})
			},
		},
	}
}

func journalCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"journal", "list"},
			Summary: "Recent journal entries (default: last 14 days)",
			Options: []OptSpec{
				{Name: "from", Value: "<YYYY-MM-DD>", Desc: "From date"},
				{Name: "to", Value: "<YYYY-MM-DD>", Desc: "To date"},
				{Name: "limit", Value: "<n>", Desc: "Max entries"},
			},
			Examples: []string{"gc journal list --from 2026-09-25"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.ListJournal(js.O("from", c.sv("from"), "to", c.sv("to"), "limit", c.sv("limit")))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					rows := js.List(js.Get(res, "data"))
					if len(rows) == 0 {
						return lines("No journal entries.")
					}
					ls := []string{}
					for _, e := range rows {
						snap := ""
						if s := js.Get(e, "activity_snapshot"); js.Truthy(s) {
							snap = fmt.Sprintf("%s done, %s comments  ", js.Str(js.Get(s, "tasks_completed")), js.Str(js.Get(s, "comments_added")))
						}
						summary := js.Or(js.Get(e, "entry", "agent_summary"), js.Get(e, "entry", "summary"), "")
						ls = append(ls, fmt.Sprintf("%s  %s%s", js.Str(js.Get(e, "date")), snap, output.OneLine(summary, 140)))
					}
					return ls
				})
			},
		},
		{
			Path:     []string{"journal", "get"},
			Summary:  "Get (or auto-generate) the journal entry for a day",
			Args:     "<YYYY-MM-DD>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc journal get 2026-10-01"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := client.GetJournalDay(c.Args[0])
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines(js.StringifyIndent(js.Or(js.Get(res, "data"), res))) })
			},
		},
		{
			Path:     []string{"journal", "summary"},
			Summary:  "Save your 2–4 sentence summary for a day",
			Args:     "<YYYY-MM-DD> [<text> | -]",
			MinArgs:  1,
			MaxArgs:  2,
			Options:  []OptSpec{{Name: "body-file", Value: "<path|->", Desc: "Read the summary from a file, or stdin with -"}},
			Examples: []string{`gc journal summary 2026-10-01 "Shipped the CLI; reviewed two PRs."`},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				body, err := c.bodyInput(c.arg(1))
				if err != nil {
					return err
				}
				res, err := client.SaveJournalSummary(c.Args[0], body)
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("saved summary for " + c.Args[0]) })
			},
		},
	}
}
