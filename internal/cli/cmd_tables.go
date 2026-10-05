package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

// People call them DATASHEETS; the wire (route, ids, errors) says "table".
// A 404 on any of these means "no such datasheet" OR "no datasheets module in
// this workspace" — deliberately the same answer.
const notFoundHint = "HTTP 404 = no such datasheet, or this workspace has no datasheets module. Don't retry."

const fieldTypes = "text | number | boolean | date | single_select | multi_select"

func tableLine(t any) string {
	return strings.Join([]string{
		js.JoinStr(js.Get(t, "id")),
		js.JoinStr(js.Get(t, "name")),
		js.Str(js.Or(js.Get(t, "row_count"), "?")) + " rows",
		fmt.Sprintf("%d fields", len(js.List(js.Get(t, "fields")))),
		initiativeOrPersonal(t),
	}, "  ")
}

func fieldLines(fields []any) []string {
	out := []string{}
	for _, f := range fields {
		optParts := []string{}
		for _, o := range js.List(js.Get(f, "options")) {
			optParts = append(optParts, js.Str(js.Get(o, "id"))+"="+js.Str(js.Get(o, "label")))
		}
		req := ""
		if js.Truthy(js.Get(f, "required")) {
			req = ", required"
		}
		line := fmt.Sprintf("- %s  %s  (%s%s)", js.Str(js.Get(f, "id")), js.Str(js.Get(f, "name")), js.Str(js.Get(f, "type")), req)
		if len(optParts) > 0 {
			line += "  options: " + strings.Join(optParts, ", ")
		}
		out = append(out, line)
	}
	return out
}

// rowLine renders a row with field NAMES and option LABELS (ids stay in `gc tables get`).
func rowLine(row any, fields []any) string {
	byID := map[string]any{}
	for _, f := range fields {
		byID[js.Str(js.Get(f, "id"))] = f
	}
	label := func(f any, v any) string {
		opts := map[string]any{}
		for _, o := range js.List(js.Get(f, "options")) {
			opts[js.Str(js.Get(o, "id"))] = js.Get(o, "label")
		}
		lookup := func(x any) (any, bool) {
			if s, ok := x.(string); ok {
				l, ok := opts[s]
				return l, ok
			}
			return nil, false
		}
		if arr, ok := js.Arr(v); ok {
			parts := []string{}
			for _, x := range arr {
				if l, ok := lookup(x); ok && !js.Nullish(l) {
					parts = append(parts, js.Str(l))
				} else {
					parts = append(parts, js.Str(x))
				}
			}
			return strings.Join(parts, ", ")
		}
		if l, ok := lookup(v); ok {
			return js.Str(l)
		}
		if s, ok := v.(string); ok {
			return output.OneLine(s, 80)
		}
		return js.Stringify(v)
	}
	cells := []string{}
	if data := js.Obj(js.Get(row, "data")); data != nil {
		for _, k := range data.Keys() {
			v, _ := data.Get(k)
			if v == nil || v == "" {
				continue
			}
			f := byID[k]
			name := k
			if f != nil && !js.Nullish(js.Get(f, "name")) {
				name = js.Str(js.Get(f, "name"))
			}
			cells = append(cells, name+"="+label(f, v))
		}
	}
	joined := strings.Join(cells, " · ")
	if joined == "" {
		joined = "(empty)"
	}
	return js.Str(js.Get(row, "id")) + "  " + joined
}

// withHint adds the 404 explanation to datasheet errors.
func withHint(res any, err error) (any, error) {
	if err != nil && strings.Contains(err.Error(), " 404:") {
		var ce *clierr.Error
		if !errors.As(err, &ce) {
			return nil, clierr.New(err.Error(), notFoundHint)
		}
	}
	return res, err
}

// parseFieldSpec reads `"Name:type[:opt1,opt2][:required]"`.
func parseFieldSpec(spec string) (*js.Object, error) {
	parts := strings.Split(spec, ":")
	name := strings.TrimSpace(parts[0])
	typ := ""
	if len(parts) > 1 {
		typ = strings.TrimSpace(parts[1])
	}
	if name == "" || typ == "" {
		return nil, clierr.Usage(fmt.Sprintf("--field \"%s\": expected \"Name:type[:opt1,opt2][:required]\".", spec))
	}
	var required any = js.Undefined
	var options any = js.Undefined
	if len(parts) > 2 {
		for _, p := range parts[2:] {
			if strings.TrimSpace(p) == "required" {
				required = true
			} else if strings.TrimSpace(p) != "" {
				o := []string{}
				for _, x := range strings.Split(p, ",") {
					if x = strings.TrimSpace(x); x != "" {
						o = append(o, x)
					}
				}
				options = o
			}
		}
	}
	return js.O("name", name, "type", typ, "options", options, "required", required), nil
}

func tableCommands() []*Command {
	return []*Command{
		{
			Path:    []string{"tables", "list"},
			Summary: "List datasheets you can see",
			Options: []OptSpec{
				{Name: "initiative", Value: "<id>", Desc: "Only this initiative"},
				{Name: "q", Value: "<text>", Desc: "Name contains"},
				{Name: "limit", Value: "<n>", Desc: "Max rows (default 100)"},
				{Name: "offset", Value: "<n>", Desc: "Skip rows"},
			},
			Examples: []string{"gc tables list"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.ListTables(js.O("initiative_id", c.sv("initiative"), "q", c.sv("q"), "limit", c.sv("limit"), "offset", c.sv("offset"))))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return listLines(res, tableLine, "No datasheets.") })
			},
		},
		{
			Path:     []string{"tables", "get"},
			Summary:  "A datasheet's schema: field ids (fld_…), types, option ids (opt_…) — read it before writing rows",
			Args:     "<table-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Examples: []string{"gc tables get <table-id>"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.GetTable(c.Args[0]))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					t := js.Get(res, "data")
					ls := []string{"# " + js.Str(js.Get(t, "name")), tableLine(t)}
					if d := js.Get(t, "description"); js.Truthy(d) {
						ls = append(ls, "", js.Str(d))
					}
					ls = append(ls, "", "Fields:")
					return append(ls, fieldLines(js.List(js.Get(t, "fields")))...)
				})
			},
		},
		{
			Path:    []string{"tables", "create"},
			Summary: "Create a datasheet with columns",
			Options: []OptSpec{
				{Name: "name", Value: "<text>", Desc: "Name (required)"},
				{Name: "description", Value: "<text>", Desc: "Description"},
				{Name: "initiative", Value: "<id>", Desc: "Initiative (visibility follows it)"},
				{Name: "field", Multiple: true, Value: `<"Name:type[:opt1,opt2][:required]">`, Desc: "Column (repeatable); type: " + fieldTypes},
				{Name: "fields-json", Value: "<json>", Desc: "Columns as JSON: [{name, type, options?, required?}]"},
			},
			Examples: []string{
				`gc tables create --name Leads --field "Company:text:required" --field "Stage:single_select:New,Won,Lost"`,
			},
			Run: func(c *Ctx) error {
				specs := []any{}
				for _, s := range c.list("field") {
					f, err := parseFieldSpec(s)
					if err != nil {
						return err
					}
					specs = append(specs, f)
				}
				jsonRaw, hasJSON := c.str("fields-json")
				if hasJSON && len(specs) > 0 {
					return clierr.Usage("Use either --field or --fields-json, not both.")
				}
				var fields any = js.Undefined
				if hasJSON {
					v, err := parseJSON(jsonRaw, "--fields-json")
					if err != nil {
						return err
					}
					fields = v
				} else if len(specs) > 0 {
					fields = specs
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				name, err := c.requireStr("name")
				if err != nil {
					return err
				}
				res, err := withHint(client.CreateTable(js.O("name", name, "description", c.sv("description"), "initiative_id", c.sv("initiative"), "fields", fields)))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					return append([]string{"created  " + tableLine(js.Get(res, "data"))}, fieldLines(js.List(js.Get(res, "data", "fields")))...)
				})
			},
		},
		{
			Path:    []string{"tables", "update"},
			Summary: "Rename, re-describe or move a datasheet (creator/owner/admin)",
			Args:    "<table-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "name", Value: "<text>", Desc: "New name"},
				{Name: "description", Value: "<text>", Desc: "New description"},
				{Name: "initiative", Value: "<id>", Desc: "Move to this initiative"},
			},
			Examples: []string{`gc tables update <table-id> --name "Leads 2026"`},
			Run: func(c *Ctx) error {
				patch := js.O("name", c.sv("name"), "description", c.sv("description"), "initiative_id", c.sv("initiative"))
				if patch.Len() == 0 {
					return clierr.Usage("Nothing to update — pass --name, --description or --initiative.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.UpdateTable(c.Args[0], patch))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("updated  " + tableLine(js.Get(res, "data"))) })
			},
		},
		{
			Path:     []string{"tables", "delete"},
			Summary:  "Delete a datasheet with all rows and comments — irreversible (needs --yes)",
			Args:     "<table-id>",
			MinArgs:  1,
			MaxArgs:  1,
			Options:  []OptSpec{{Name: "yes", Bool: true, Desc: "Confirm the irreversible delete"}},
			Examples: []string{"gc tables delete <table-id> --yes"},
			Run: func(c *Ctx) error {
				if !c.bool("yes") {
					return clierr.Usage("Deleting a datasheet is irreversible — add --yes to confirm.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.DeleteTable(c.Args[0]))
				if err != nil {
					return err
				}
				return c.Out.Emit(js.Or(res, js.O("deleted", c.Args[0])), func() []string { return lines("deleted datasheet " + c.Args[0]) })
			},
		},
		{
			Path:    []string{"tables", "rows"},
			Summary: "Read rows (one page, or every match with --all)",
			Args:    "<table-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "filter", Value: "<json>", Desc: `[{"field":"fld_…","op":"eq","value":"opt_…"}], AND-combined`},
				{Name: "sort", Value: "<fld_x:asc|desc>", Desc: "Sort by a field"},
				{Name: "q", Value: "<text>", Desc: "Full-text match"},
				{Name: "all", Bool: true, Desc: "Walk every page (complete result)"},
				{Name: "limit", Value: "<n>", Desc: "Page size (server max 200)"},
				{Name: "offset", Value: "<n>", Desc: "Skip rows"},
			},
			Details:  "Filter select fields by option id (opt_…), never by label. Operators: gc guide datasheets",
			Examples: []string{"gc tables rows <table-id> --all", `gc tables rows <table-id> --filter '[{"field":"fld_ab12","op":"eq","value":"opt_cd34"}]'`},
			Run:      runTableRows,
		},
		{
			Path:    []string{"tables", "add-rows"},
			Summary: "Add ≤100 rows (all or nothing); each row keyed by field id, select values are option ids",
			Args:    "<table-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "data", Value: "<json>", Desc: `[{"fld_…": value, …}, …]`},
				{Name: "data-file", Value: "<path|->", Desc: "Same JSON from a file, or stdin with -"},
			},
			Examples: []string{`gc tables add-rows <table-id> --data '[{"fld_ab12":"Acme","fld_cd34":"opt_ef56"}]'`},
			Run: func(c *Ctx) error {
				v, err := c.jsonInput("data", "data-file")
				if err != nil {
					return err
				}
				rows, ok := js.Arr(v)
				if !ok || len(rows) == 0 {
					return clierr.Usage("Rows must be a non-empty JSON array of objects keyed by field id.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.CreateRows(c.Args[0], rows))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines(fmt.Sprintf("added %s row(s)", countOf(js.Get(res, "data"), rows))) })
			},
		},
		{
			Path:    []string{"tables", "update-rows"},
			Summary: "Change ≤100 rows (all or nothing): [{id, data}], changed fields only, null clears",
			Args:    "<table-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "data", Value: "<json>", Desc: `[{"id":"<row-id>","data":{"fld_…": value}}]`},
				{Name: "data-file", Value: "<path|->", Desc: "Same JSON from a file, or stdin with -"},
			},
			Examples: []string{`gc tables update-rows <table-id> --data '[{"id":"<row-id>","data":{"fld_cd34":"opt_gh78"}}]'`},
			Run: func(c *Ctx) error {
				v, err := c.jsonInput("data", "data-file")
				if err != nil {
					return err
				}
				rows, ok := js.Arr(v)
				if !ok || len(rows) == 0 {
					return clierr.Usage("Expected a non-empty JSON array of {id, data}.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.UpdateRows(c.Args[0], rows))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines(fmt.Sprintf("updated %s row(s)", countOf(js.Get(res, "data"), rows))) })
			},
		},
		{
			Path:     []string{"tables", "delete-rows"},
			Summary:  "Delete ≤100 rows by id (unknown ids are skipped)",
			Args:     "<table-id> <row-id>…",
			MinArgs:  2,
			MaxArgs:  101,
			Examples: []string{"gc tables delete-rows <table-id> <row-id> <row-id>"},
			Run: func(c *Ctx) error {
				id, ids := c.Args[0], c.Args[1:]
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.DeleteRows(id, ids))
				if err != nil {
					return err
				}
				return c.Out.Emit(js.Or(res, js.O("deleted", ids)), func() []string {
					return lines(fmt.Sprintf("deleted %s row(s)", js.Str(js.Or(js.Get(res, "data", "deleted"), len(ids)))))
				})
			},
		},
		{
			Path:    []string{"tables", "field-add"},
			Summary: "Append a column",
			Args:    "<table-id>",
			MinArgs: 1,
			MaxArgs: 1,
			Options: []OptSpec{
				{Name: "name", Value: "<text>", Desc: "Column name (required)"},
				{Name: "type", Value: "<type>", Desc: fieldTypes + " (required, immutable)"},
				{Name: "option", Multiple: true, Value: "<label>", Desc: "Select option label (repeatable)"},
				{Name: "required", Bool: true, Desc: "Mark the column required"},
			},
			Examples: []string{"gc tables field-add <table-id> --name Stage --type single_select --option New --option Won"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				name, err := c.requireStr("name")
				if err != nil {
					return err
				}
				typ, err := c.requireStr("type")
				if err != nil {
					return err
				}
				var options any = js.Undefined
				if o := c.list("option"); len(o) > 0 {
					options = o
				}
				var required any = js.Undefined
				if c.bool("required") {
					required = true
				}
				res, err := withHint(client.AddField(c.Args[0], js.O("name", name, "type", typ, "options", options, "required", required)))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					fields := js.List(js.Get(res, "data", "fields"))
					if js.Nullish(js.Get(res, "data", "fields")) {
						fields = []any{js.Get(res, "data")}
					}
					kept := []any{}
					for _, f := range fields {
						if js.Truthy(f) {
							kept = append(kept, f)
						}
					}
					s := strings.Join(fieldLines(kept), "\n")
					if s == "" {
						s = "field added"
					}
					return lines(s)
				})
			},
		},
		{
			Path:    []string{"tables", "field-update"},
			Summary: "Edit a column; --options-json is the COMPLETE new option list ({id,label} keeps, no id adds)",
			Args:    "<table-id> <field-id>",
			MinArgs: 2,
			MaxArgs: 2,
			Options: []OptSpec{
				{Name: "name", Value: "<text>", Desc: "New name"},
				{Name: "required", Value: "<true|false>", Desc: "Required flag"},
				{Name: "position", Value: "<n>", Desc: "Column position"},
				{Name: "options-json", Value: "<json>", Desc: `[{"id":"opt_…","label":"…"},{"label":"New"}]`},
			},
			Examples: []string{`gc tables field-update <table-id> fld_ab12 --name "Company name"`},
			Run: func(c *Ctx) error {
				var required any = js.Undefined
				if req, ok := c.str("required"); ok {
					if req != "true" && req != "false" {
						return clierr.Usage("--required must be true or false.")
					}
					required = req == "true"
				}
				position, err := num(c.sv("position"), "position", false)
				if err != nil {
					return err
				}
				var options any = js.Undefined
				if raw, ok := c.str("options-json"); ok {
					if options, err = parseJSON(raw, "--options-json"); err != nil {
						return err
					}
				}
				patch := js.O("name", c.sv("name"), "required", required, "position", position, "options", options)
				if patch.Len() == 0 {
					return clierr.Usage("Nothing to update.")
				}
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.UpdateField(c.Args[0], c.Args[1], patch))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines("updated field " + c.Args[1]) })
			},
		},
		{
			Path:     []string{"tables", "field-delete"},
			Summary:  "Remove a column",
			Args:     "<table-id> <field-id>",
			MinArgs:  2,
			MaxArgs:  2,
			Examples: []string{"gc tables field-delete <table-id> fld_ab12"},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				res, err := withHint(client.DeleteField(c.Args[0], c.Args[1]))
				if err != nil {
					return err
				}
				return c.Out.Emit(js.Or(res, js.O("deleted", c.Args[1])), func() []string { return lines("deleted field " + c.Args[1]) })
			},
		},
		{
			Path:     []string{"tables", "comment"},
			Summary:  "Comment on a datasheet: say what you changed and why",
			Args:     "<table-id> [<body> | -]",
			MinArgs:  1,
			MaxArgs:  2,
			Options:  []OptSpec{{Name: "body-file", Value: "<path|->", Desc: "Read the body from a file, or stdin with -"}},
			Examples: []string{`gc tables comment <table-id> "Imported 40 leads from the CSV."`},
			Run: func(c *Ctx) error {
				client, err := c.Client()
				if err != nil {
					return err
				}
				body, err := c.bodyInput(c.arg(1))
				if err != nil {
					return err
				}
				res, err := withHint(client.CreateTableComment(c.Args[0], body))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string {
					return lines(fmt.Sprintf("commented on datasheet %s · %s", c.Args[0], output.FmtTime(js.Get(res, "data", "created_at"))))
				})
			},
		},
	}
}

// countOf is `(res.data ?? rows).length`.
func countOf(data any, rows []any) string {
	if js.Nullish(data) {
		return fmt.Sprint(len(rows))
	}
	if a, ok := js.Arr(data); ok {
		return fmt.Sprint(len(a))
	}
	if s, ok := data.(string); ok {
		return fmt.Sprint(len([]rune(s)))
	}
	return "undefined"
}

func runTableRows(c *Ctx) error {
	var filter any = js.Undefined
	if raw, ok := c.str("filter"); ok {
		v, err := parseJSON(raw, "--filter")
		if err != nil {
			return err
		}
		filter = js.Stringify(v)
	}
	client, err := c.Client()
	if err != nil {
		return err
	}
	id := c.Args[0]
	all := c.bool("all")
	var res any
	if all {
		res, err = withHint(client.ListAllRows(id, js.O("filter", filter, "sort", c.sv("sort"), "q", c.sv("q")), 25))
	} else {
		res, err = withHint(client.ListRows(id, js.O("filter", filter, "sort", c.sv("sort"), "q", c.sv("q"), "limit", c.sv("limit"), "offset", c.sv("offset"))))
	}
	if err != nil {
		return err
	}
	if c.Out.Raw() {
		return c.Out.Emit(res, func() []string { return nil })
	}
	table, err := withHint(client.GetTable(id))
	if err != nil {
		return err
	}
	fields := js.List(js.Get(table, "data", "fields"))
	return c.Out.Emit(res, func() []string {
		rows := js.List(js.Get(res, "data"))
		ls := []string{}
		for _, r := range rows {
			ls = append(ls, rowLine(r, fields))
		}
		if len(rows) == 0 {
			ls = lines("No rows.")
		}
		if js.Truthy(js.Get(res, "meta", "truncated")) {
			ls = append(ls, fmt.Sprintf("-- truncated after %d of %s matching rows", len(rows), js.Str(js.Get(res, "meta", "matched"))))
		} else if !all {
			if f := output.PageFooter(len(rows), js.Get(res, "meta"), "Next page: --offset {next}, or --all"); f != "" {
				ls = append(ls, f)
			}
		}
		return ls
	})
}
