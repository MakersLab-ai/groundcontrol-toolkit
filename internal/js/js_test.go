package js

import (
	"math"
	"testing"
	"time"
)

func TestParseKeepsOrderAndStringifyMatchesJSON(t *testing.T) {
	v, err := Parse([]byte(`{"b":1,"a":[1,2.50,{"z":null,"y":"<&> "}],"c":{},"d":[],"e":"q\"\\\n\t\u0001","b":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Stringify(v); got != "{\"b\":3,\"a\":[1,2.5,{\"z\":null,\"y\":\"<&> \"}],\"c\":{},\"d\":[],\"e\":\"q\\\"\\\\\\n\\t\\u0001\"}" {
		t.Fatalf("compact: %s", got)
	}
	want := "{\n  \"b\": 3,\n  \"a\": [\n    1,\n    2.5,\n    {\n      \"z\": null,\n      \"y\": \"<&> \"\n    }\n  ],\n  \"c\": {},\n  \"d\": [],\n  \"e\": \"q\\\"\\\\\\n\\t\\u0001\"\n}"
	if got := StringifyIndent(v); got != want {
		t.Fatalf("indent:\n%s", got)
	}
	for _, bad := range []string{"", "{", `{"a":1} x`, "{nope}"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestObjectAndUndefined(t *testing.T) {
	o := O("a", 1, "skip", Undefined, "b", nil)
	if Stringify(o) != `{"a":1,"b":null}` {
		t.Fatal(Stringify(o))
	}
	o.Set("a", 2)
	o.Set("c", "x")
	o.Delete("b")
	if Stringify(o) != `{"a":2,"c":"x"}` {
		t.Fatal(Stringify(o))
	}
	if Get(o, "nope", "deeper") != Undefined || Get(o, "a") != 2 {
		t.Fatal("Get")
	}
	if Or(Undefined, nil, "x") != "x" || Or(Undefined, nil) != nil || Or(0.0, 5.0) != 0.0 {
		t.Fatal("Or")
	}
}

func TestNumbersAndCoercions(t *testing.T) {
	cases := map[float64]string{0: "0", 3: "3", 0.5: "0.5", -2.25: "-2.25", 1e21: "1e+21", 1.5e-7: "1.5e-7", 123456789: "123456789"}
	for f, want := range cases {
		if got := NumberString(f); got != want {
			t.Fatalf("%v → %s, want %s", f, got, want)
		}
	}
	nums := map[string]float64{"3": 3, " 4 ": 4, "1e3": 1000, "0x10": 16, "": 0, "-1.5": -1.5, ".5": 0.5}
	for s, want := range nums {
		if got := ParseNumber(s); got != want {
			t.Fatalf("%q → %v", s, got)
		}
	}
	for _, s := range []string{"many", "1,5", "inf", "NaN", "1_000", "0x"} {
		if !math.IsNaN(ParseNumber(s)) {
			t.Fatalf("%q is a number", s)
		}
	}
	if Str(nil) != "null" || Str(Undefined) != "undefined" || JoinStr(nil) != "" || Str([]any{1.0, nil, "a"}) != "1,,a" {
		t.Fatal("Str")
	}
	if Truthy("") || Truthy(0.0) || Truthy(nil) || !Truthy("0") || !Truthy(NewObject()) {
		t.Fatal("Truthy")
	}
}

func TestParseDate(t *testing.T) {
	ok := map[string]string{
		"2026-10-02T08:00:00Z":             "2026-10-02T08:00:00.000Z",
		"2026-10-02T10:00:00+02:00":        "2026-10-02T08:00:00.000Z",
		"2026-10-02":                       "2026-10-02T00:00:00.000Z",
		"2026-10-02T11:00:00.123456+00:00": "2026-10-02T11:00:00.123Z",
		"2026-10-02 08:00:00+00":           "2026-10-02T08:00:00.000Z",
	}
	for in, want := range ok {
		d, good := ParseDate(in)
		if !good || ISO(d) != want {
			t.Fatalf("%s → %v %s", in, good, ISO(d))
		}
	}
	local, good := ParseDate("2026-10-02T08:00:00")
	if !good || !local.Equal(time.Date(2026, 10, 2, 8, 0, 0, 0, time.Local)) {
		t.Fatal("zone-less date-time is local time, as in JS")
	}
	for _, bad := range []string{"2026-13-45", "2026-02-31", "yesterday", "5", "2026-10-02T25:00:00Z"} {
		if _, good := ParseDate(bad); good {
			t.Fatalf("%s parsed", bad)
		}
	}
}
