package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

var (
	relRe     = regexp.MustCompile(`(?i)^(\d+)\s*([smhdw])$`)
	isoPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
	unitDur   = map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}
)

// parseSince accepts an ISO timestamp or a relative age (90s, 15m, 2h, 1d, 1w)
// and always returns a canonical ISO string — the server parses it with
// `new Date()`, so we fail here, with a usable message, instead of there.
func parseSince(input string, now time.Time) (string, error) {
	value := strings.TrimSpace(input)
	if m := relRe.FindStringSubmatch(value); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return js.ISO(now.Add(-time.Duration(n) * unitDur[strings.ToLower(m[2])])), nil
	}
	// Only things that look like dates.
	if isoPrefix.MatchString(value) {
		if t, ok := js.ParseDate(value); ok {
			return js.ISO(t), nil
		}
	}
	return "", clierr.Usage(fmt.Sprintf("Invalid --since value \"%s\".", input),
		"Use an ISO timestamp (2026-10-02T08:00:00Z) or an age: 90s, 15m, 2h, 1d, 1w.")
}
