package js

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The ISO-8601 forms Date.parse accepts (plus the "date time" with a space
// that Postgres prints). A date alone is UTC; a date-time without an offset
// is local time — both as in JavaScript.
var isoRe = regexp.MustCompile(`^(\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:[T ](\d{2}):(\d{2})(?::(\d{2})(?:[.,](\d+))?)?)?\s*(Z|z|[+-]\d{2}(?::?\d{2})?)?$`)

// ParseDate is Date.parse for ISO timestamps; ok=false where JS gives NaN.
func ParseDate(s string) (time.Time, bool) {
	m := isoRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}, false
	}
	atoi := func(x string, def int) int {
		if x == "" {
			return def
		}
		n, _ := strconv.Atoi(x)
		return n
	}
	year, month, day := atoi(m[1], 0), atoi(m[2], 1), atoi(m[3], 1)
	hour, min, sec := atoi(m[4], 0), atoi(m[5], 0), atoi(m[6], 0)
	ms := 0
	if m[7] != "" {
		frac := (m[7] + "000")[:3]
		ms = atoi(frac, 0)
	}
	if month < 1 || month > 12 || day < 1 || hour > 24 || min > 59 || sec > 59 {
		return time.Time{}, false
	}
	if hour == 24 && (min != 0 || sec != 0 || ms != 0) {
		return time.Time{}, false
	}
	loc := time.UTC
	hasTime := m[4] != ""
	switch zone := m[8]; {
	case zone == "Z" || zone == "z":
	case zone != "":
		sign := 1
		if zone[0] == '-' {
			sign = -1
		}
		digits := strings.ReplaceAll(zone[1:], ":", "")
		h := atoi(digits[:2], 0)
		mm := 0
		if len(digits) >= 4 {
			mm = atoi(digits[2:4], 0)
		}
		if h > 23 || mm > 59 {
			return time.Time{}, false
		}
		loc = time.FixedZone("", sign*(h*3600+mm*60))
	case hasTime:
		loc = time.Local
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, ms*int(time.Millisecond), loc)
	// Reject 2026-02-31 instead of rolling it into March.
	check := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if check.Day() != day || int(check.Month()) != month {
		return time.Time{}, false
	}
	return t, true
}

// ISO is Date#toISOString: UTC, millisecond precision.
func ISO(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

// NowISO is new Date().toISOString().
func NowISO() string { return ISO(time.Now()) }
