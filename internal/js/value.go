package js

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Get walks object keys (and numeric array indices): Get(res, "data", "user").
// Anything missing on the way is Undefined, like optional chaining.
func Get(v any, path ...string) any {
	cur := v
	for _, p := range path {
		switch x := cur.(type) {
		case *Object:
			nv, ok := x.Get(p)
			if !ok {
				return Undefined
			}
			cur = nv
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(x) {
				return Undefined
			}
			cur = x[i]
		default:
			return Undefined
		}
	}
	return cur
}

// Nullish: null or undefined.
func Nullish(v any) bool { return v == nil || v == Undefined }

// Or is `a ?? b ?? …`: the first non-nullish value, else the last one.
func Or(vs ...any) any {
	for _, v := range vs {
		if !Nullish(v) {
			return v
		}
	}
	if len(vs) == 0 {
		return Undefined
	}
	return vs[len(vs)-1]
}

// Truthy is JavaScript truthiness.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil, undefinedT:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number, float64, int, int64, float32:
		f, _ := AsNumber(x)
		return f != 0 && !math.IsNaN(f)
	}
	return true
}

// AsNumber returns the value if it is a JSON number (typeof x === 'number').
func AsNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// Number is JavaScript's Number(v).
func Number(v any) float64 {
	if f, ok := AsNumber(v); ok {
		return f
	}
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		return ParseNumber(x)
	}
	return math.NaN()
}

var (
	decimalRe = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
	radixRe   = regexp.MustCompile(`^0([xXoObB])([0-9a-fA-F]+)$`)
)

// ParseNumber is Number(string): NaN when the text is not a number literal.
func ParseNumber(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if decimalRe.MatchString(t) {
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			// out of range: JS gives ±Infinity / 0
			if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return f
			}
			return math.NaN()
		}
		return f
	}
	if m := radixRe.FindStringSubmatch(t); m != nil {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[m[1][0]]
		n, err := strconv.ParseUint(m[2], base, 64)
		if err != nil {
			return math.NaN()
		}
		return float64(n)
	}
	return math.NaN()
}

// Str is String(v) / a template literal `${v}`.
func Str(v any) string {
	switch x := v.(type) {
	case undefinedT:
		return "undefined"
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case *Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = JoinStr(e)
		}
		return strings.Join(parts, ",")
	}
	if f, ok := AsNumber(v); ok {
		return NumberString(f)
	}
	return Stringify(v)
}

// JoinStr is how Array#join renders an element: null/undefined become "".
func JoinStr(v any) string {
	if Nullish(v) {
		return ""
	}
	return Str(v)
}

// S is a string field or "" — for the common `x ?? ”` in templates.
func S(v any) string { return JoinStr(v) }

// Arr returns the array, or nil when v is not one (Array.isArray).
func Arr(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

// List is `v ?? []` for arrays: the elements when v is an array, else none.
func List(v any) []any {
	a, _ := v.([]any)
	return a
}

// Obj returns the object, or nil.
func Obj(v any) *Object {
	o, _ := v.(*Object)
	return o
}

// IsString reports typeof v === 'string'.
func IsString(v any) bool {
	_, ok := v.(string)
	return ok
}
