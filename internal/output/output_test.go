package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

func parse(t *testing.T, s string) any {
	t.Helper()
	v, err := js.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExtractField(t *testing.T) {
	res := parse(t, `{"data":{"id":"t1","title":"Hello","tags":[{"n":"a"},{"n":"b"}],"nested":{"x":null}},"meta":{"total":3}}`)
	cases := map[string]string{"data.title": "Hello", "title": "Hello", "data.tags.1.n": "b", "tags.-1.n": "b", "meta.total": "3", "nested.x": "null", "tags": "[\n  {\n    \"n\": \"a\"\n  },\n  {\n    \"n\": \"b\"\n  }\n]"}
	for path, want := range cases {
		v, err := ExtractField(res, path)
		if err != nil || FormatField(v) != want {
			t.Fatalf("%s → %v %q", path, err, FormatField(v))
		}
	}
	for _, path := range []string{"data.nope", "data.tags.x", "title.length"} {
		if _, err := ExtractField(res, path); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if FormatField("a\nb") != "a\nb" {
		t.Fatal("strings print raw")
	}
}

func TestFormatting(t *testing.T) {
	line := TaskLine(parse(t, `{"id":"11111111-2222-3333-4444-555555555555","status":"in_progress","priority":"high","title":"Fix it","story_points":3,"assignee":{"display_name":"Ada"},"initiative":{"name":"Core"}}`))
	if line != "11111111-2222-3333-4444-555555555555  in_progress  high      Fix it  [3sp]  @Ada  #Core" {
		t.Fatal(line)
	}
	if l := TaskLine(parse(t, `{"id":"x","status":"todo","priority":"low","title":"T","for":"principal"}`)); !strings.HasPrefix(l, "[principal]  x") {
		t.Fatal(l)
	}
	if PageFooter(20, parse(t, `{"total":20}`), "x") != "" {
		t.Fatal("no footer when complete")
	}
	if f := PageFooter(20, parse(t, `{"total":154,"offset":0}`), "Next page: --offset {next}"); f != "-- showing 1–20 of 154. Next page: --offset 20" {
		t.Fatal(f)
	}
	if b := CommentBlock(parse(t, `{"author":{"display_name":"Bo"},"created_at":"2026-10-02T08:15:30Z","body":"hi\n"}`)); b != "--- Bo · 2026-10-02 08:15Z\nhi" {
		t.Fatal(b)
	}
	if FmtTime(js.Undefined) != "-" || FmtTime("garbage") != "garbage" {
		t.Fatal("FmtTime")
	}
	if HumanSize(2048.0) != "2.0 KB" || HumanSize(12.0) != "12 B" || HumanSize(js.Undefined) != "?" || HumanSize(5*1024*1024.0) != "5.0 MB" {
		t.Fatal("HumanSize")
	}
	if OneLine("a\n\n b   c", 160) != "a b c" || OneLine("abcdef", 4) != "abc…" {
		t.Fatal("OneLine")
	}
}

func TestEmitModes(t *testing.T) {
	res := parse(t, `{"data":{"title":"T"},"warnings":[{"code":"w1","message":"careful"}]}`)
	var out, errb bytes.Buffer
	o := &Output{Stdout: &out, Stderr: &errb}
	o.Emit(res, func() []string { return []string{"line"} })
	if out.String() != "line\n" || errb.String() != "warning: w1 — careful\n" {
		t.Fatalf("text: %q %q", out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	o.JSON = true
	o.Emit(res, func() []string { return nil })
	if !strings.HasPrefix(out.String(), "{\n  \"data\": {") || errb.Len() != 0 {
		t.Fatalf("json: %q %q", out.String(), errb.String())
	}
	out.Reset()
	o.JSON, o.Field = false, "title"
	o.Emit(res, func() []string { return nil })
	if out.String() != "T\n" {
		t.Fatalf("field: %q", out.String())
	}
}
