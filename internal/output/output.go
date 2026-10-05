// Package output prints API results three ways — compact text (default), the
// raw response (--json) or one value (--field) — and holds the text formatters.
package output

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

type Output struct {
	JSON   bool
	Field  string
	Stdout io.Writer
	Stderr io.Writer
}

// Raw: --json or --field (the caller wants the API's shape, not text).
func (o *Output) Raw() bool { return o.JSON || o.Field != "" }

// Emit prints an API result: raw JSON (--json), one value (--field), or the
// compact text. A failed write is an error — callers that persist state
// (cursors) must only do so after Emit succeeded.
func (o *Output) Emit(result any, text func() []string) error {
	if o.Field != "" {
		o.Warnings(result)
		v, err := ExtractField(result, o.Field)
		if err != nil {
			return err
		}
		return o.Line(FormatField(v))
	}
	if o.JSON {
		return o.Line(js.StringifyIndent(result))
	}
	o.Warnings(result)
	s := strings.Join(text(), "\n")
	if s == "" {
		return nil
	}
	return o.Line(s)
}

func (o *Output) Line(s string) error {
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	_, err := io.WriteString(o.Stdout, s)
	return err
}

func (o *Output) Err(s string) {
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	io.WriteString(o.Stderr, s)
}

// Warnings: write routes return non-fatal `warnings` (e.g. scrum_expects_review) — never swallow them.
func (o *Output) Warnings(result any) {
	w, ok := js.Arr(js.Get(result, "warnings"))
	if !ok {
		return
	}
	for _, item := range w {
		msg := ""
		if m := js.Get(item, "message"); js.Truthy(m) {
			msg = " — " + js.Str(m)
		}
		o.Err("warning: " + js.S(js.Get(item, "code")) + msg)
	}
}

type missing struct{}

// ExtractField resolves `--field a.b.0.c`. Looked up from the root of the API
// response first; when that misses and the response has a `data` envelope,
// from inside it — so both `--field data.title` and `--field title` work.
func ExtractField(root any, path string) (any, error) {
	parts := []string{}
	for _, p := range strings.Split(path, ".") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	walk := func(start any) any {
		cur := start
		for _, part := range parts {
			if js.Nullish(cur) {
				return missing{}
			}
			switch x := cur.(type) {
			case []any:
				f := js.ParseNumber(part)
				if math.IsNaN(f) || f != math.Trunc(f) {
					return missing{}
				}
				i := int(f)
				if i < 0 {
					i += len(x)
				}
				if i < 0 || i >= len(x) {
					return missing{}
				}
				cur = x[i]
			case *js.Object:
				v, ok := x.Get(part)
				if !ok {
					return missing{}
				}
				cur = v
			default:
				return missing{}
			}
			if cur == js.Undefined {
				return missing{}
			}
		}
		return cur
	}
	v := walk(root)
	if _, miss := v.(missing); miss && !strings.HasPrefix(path, "data.") {
		if o := js.Obj(root); o != nil {
			if data, ok := o.Get("data"); ok {
				v = walk(data)
			}
		}
	}
	if _, miss := v.(missing); miss {
		return nil, clierr.New(fmt.Sprintf("Field \"%s\" not found in the response.", path), "Run the same command with --json to see the shape.")
	}
	return v, nil
}

// FormatField: strings raw, everything else as indented JSON.
func FormatField(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return js.StringifyIndent(v)
}

// ─── formatting helpers ───────────────────────────────────────────────────────

// FmtTime is `2026-10-02 08:15Z` — UTC, minute precision, unambiguous for agents.
func FmtTime(iso any) string {
	s, ok := iso.(string)
	if !ok || s == "" {
		return "-"
	}
	t, ok := js.ParseDate(s)
	if !ok {
		return s
	}
	return strings.Replace(js.ISO(t)[:16], "T", " ", 1) + "Z"
}

// OneLine collapses whitespace and cuts at max characters with "…".
func OneLine(text any, max int) string {
	s := strings.Join(strings.FieldsFunc(js.S(text), isJSSpace), " ")
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		return string(r[:max-1]) + "…"
	}
	return s
}

func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }

// TrimEnd is String#trimEnd.
func TrimEnd(s string) string { return strings.TrimRightFunc(s, isJSSpace) }

// PadEnd pads with spaces to n characters (never truncates).
func PadEnd(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	return s
}

// PadStart is String#padStart with spaces.
func PadStart(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return strings.Repeat(" ", n-c) + s
	}
	return s
}

// Pad renders a value (nullish → "-") padded to n.
func Pad(v any, n int) string {
	s := "-"
	if !js.Nullish(v) {
		s = js.Str(v)
	}
	return PadEnd(s, n)
}

// Name is display_name ?? name ?? null.
func Name(m any) any {
	return js.Or(js.Get(m, "display_name"), js.Get(m, "name"), nil)
}

// Join drops empty parts (filter(Boolean)) and joins.
func Join(sep string, parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// TaskLine is one line per task: id, status, priority, title, points, assignee, initiative.
func TaskLine(t any) string {
	parts := []string{js.S(js.Get(t, "id")), Pad(js.Get(t, "status"), 11), Pad(js.Get(t, "priority"), 8), js.S(js.Get(t, "title"))}
	if sp := js.Get(t, "story_points"); !js.Nullish(sp) {
		parts = append(parts, "["+js.Str(sp)+"sp]")
	}
	if who := Name(js.Get(t, "assignee")); js.Truthy(who) {
		parts = append(parts, "@"+js.Str(who))
	}
	if n := js.Get(t, "initiative", "name"); js.Truthy(n) {
		parts = append(parts, "#"+js.Str(n))
	}
	if js.Get(t, "for") == "principal" {
		parts = append([]string{"[principal]"}, parts...)
	}
	return strings.Join(parts, "  ")
}

// PageFooter says "showing 1–20 of 154" when a list is truncated — a truncated list must say so.
func PageFooter(shown int, meta any, hint string) string {
	total, ok := js.AsNumber(js.Get(meta, "total"))
	if !ok || total <= float64(shown) {
		return ""
	}
	offset := 0.0
	if o, ok := js.AsNumber(js.Get(meta, "offset")); ok {
		offset = o
	}
	next := js.NumberString(offset + float64(shown))
	return fmt.Sprintf("-- showing %s–%s of %s. %s", js.NumberString(offset+1), next, js.NumberString(total), strings.Replace(hint, "{next}", next, 1))
}

// CommentBlock renders one comment with an author/time header.
func CommentBlock(c any) string {
	who := js.Str(js.Or(Name(js.Get(c, "author")), js.Get(c, "author_id"), "Unknown"))
	kind := ""
	if et := js.Get(c, "event_type"); js.Truthy(et) && et != "comment" {
		kind = " · " + js.Str(et)
	}
	body := TrimEnd(js.Str(js.Or(js.Get(c, "body"), js.Get(c, "content"), "")))
	return fmt.Sprintf("--- %s · %s%s\n%s", who, FmtTime(js.Get(c, "created_at")), kind, body)
}

// HumanSize renders a byte count.
func HumanSize(bytes any) string {
	n := js.Number(bytes)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return "?"
	}
	switch {
	case n < 1024:
		return js.NumberString(n) + " B"
	case n < 1024*1024:
		return strconv.FormatFloat(n/1024, 'f', 1, 64) + " KB"
	}
	return strconv.FormatFloat(n/1024/1024, 'f', 1, 64) + " MB"
}
