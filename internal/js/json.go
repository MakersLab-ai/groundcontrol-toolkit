// Package js gives the CLI the JavaScript value semantics its output formats
// were defined with: JSON objects that keep their key order (the server's
// order, profile order in config.json, a datasheet row's field order), the
// JSON.stringify layout, and the small coercions (`??`, truthiness, String())
// the text formatters rely on.
package js

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type undefinedT struct{}

// Undefined is a missing value — distinct from JSON null (nil). Objects built
// with O() and the JSON writer skip it, like JSON.stringify skips undefined.
var Undefined any = undefinedT{}

// Object is a JSON object that remembers insertion order.
type Object struct {
	keys []string
	vals map[string]any
}

func NewObject() *Object { return &Object{vals: map[string]any{}} }

// O builds an object from key/value pairs, skipping Undefined values.
func O(kv ...any) *Object {
	o := NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func (o *Object) Get(k string) (any, bool) {
	if o == nil {
		return Undefined, false
	}
	v, ok := o.vals[k]
	if !ok {
		return Undefined, false
	}
	return v, true
}

// Set replaces the value in place (keeping the key's position) or appends it.
// Setting Undefined deletes the key.
func (o *Object) Set(k string, v any) {
	if v == Undefined {
		o.Delete(k)
		return
	}
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Clone is a shallow copy ({ ...o }).
func (o *Object) Clone() *Object {
	c := NewObject()
	for _, k := range o.Keys() {
		c.Set(k, o.vals[k])
	}
	return c
}

func (o *Object) MarshalJSON() ([]byte, error) { return []byte(Stringify(o)), nil }

// Parse decodes JSON into nil, bool, string, json.Number, []any and *Object.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("unexpected data after the JSON value")
		}
		return nil, err
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err == io.EOF {
		return nil, errors.New("unexpected end of JSON input")
	}
	if err != nil {
		return nil, err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch d {
	case '{':
		o := NewObject()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := kt.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key %v", kt)
			}
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			o.Set(key, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return o, nil
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unexpected %v", d)
}

// Stringify is JSON.stringify(v).
func Stringify(v any) string {
	var b strings.Builder
	write(&b, v, "", "")
	return b.String()
}

// StringifyIndent is JSON.stringify(v, null, 2).
func StringifyIndent(v any) string {
	var b strings.Builder
	write(&b, v, "  ", "")
	return b.String()
}

func write(b *strings.Builder, v any, indent, cur string) {
	switch x := v.(type) {
	case nil, undefinedT:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		quote(b, x)
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil {
			b.WriteString(string(x))
		} else {
			b.WriteString(numberJSON(f))
		}
	case float64:
		b.WriteString(numberJSON(x))
	case float32:
		b.WriteString(numberJSON(float64(x)))
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case []any:
		writeArray(b, len(x), func(i int) any { return x[i] }, indent, cur)
	case []string:
		writeArray(b, len(x), func(i int) any { return x[i] }, indent, cur)
	case []*Object:
		writeArray(b, len(x), func(i int) any { return x[i] }, indent, cur)
	case *Object:
		if x == nil {
			b.WriteString("null")
			return
		}
		writeObject(b, x.keys, func(k string) any { return x.vals[k] }, indent, cur)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		writeObject(b, keys, func(k string) any { return x[k] }, indent, cur)
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			b.WriteString("null")
			return
		}
		parsed, err := Parse(raw)
		if err != nil {
			b.Write(raw)
			return
		}
		write(b, parsed, indent, cur)
	}
}

func writeArray(b *strings.Builder, n int, at func(int) any, indent, cur string) {
	if n == 0 {
		b.WriteString("[]")
		return
	}
	inner := cur + indent
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if indent != "" {
			b.WriteByte('\n')
			b.WriteString(inner)
		}
		write(b, at(i), indent, inner)
	}
	if indent != "" {
		b.WriteByte('\n')
		b.WriteString(cur)
	}
	b.WriteByte(']')
}

func writeObject(b *strings.Builder, keys []string, at func(string) any, indent, cur string) {
	inner := cur + indent
	n := 0
	b.WriteByte('{')
	for _, k := range keys {
		v := at(k)
		if v == Undefined {
			continue
		}
		if n > 0 {
			b.WriteByte(',')
		}
		if indent != "" {
			b.WriteByte('\n')
			b.WriteString(inner)
		}
		quote(b, k)
		b.WriteByte(':')
		if indent != "" {
			b.WriteByte(' ')
		}
		write(b, v, indent, inner)
		n++
	}
	if n > 0 && indent != "" {
		b.WriteByte('\n')
		b.WriteString(cur)
	}
	b.WriteByte('}')
}

// quote escapes like JSON.stringify: only ", \ and control characters.
func quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func numberJSON(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	return NumberString(f)
}

// NumberString is JavaScript's Number#toString.
func NumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go: 1.5e-07, JS: 1.5e-7
		if i := strings.IndexByte(s, 'e'); i >= 0 {
			mant, exp := s[:i], s[i+1:]
			sign := exp[:1]
			digits := strings.TrimLeft(exp[1:], "0")
			if digits == "" {
				digits = "0"
			}
			s = mant + "e" + sign + digits
		}
		return s
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
