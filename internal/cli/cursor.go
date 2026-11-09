package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

// workspaceSlot names the workspace a resolved key belongs to: the profile
// name, or "key-<hash>" for a key from --token/GC_API_KEY (never the key).
// `gc listen` names its cursor files with it, and `gc changes --cursor-file`
// keys its per-workspace entries with it.
func workspaceSlot(a config.Resolved, key string) (name string, isProfile bool) {
	if a.KeySource == "profile" && a.Profile != "" {
		return a.Profile, true
	}
	sum := sha256.Sum256([]byte(key))
	return "key-" + hex.EncodeToString(sum[:])[:12], false
}

// A --cursor-file holds ONE cursor PER WORKSPACE, as a JSON object
// {"<slot>": "<server cursor>", …}. One poller that works in several
// workspaces (an agent with several profiles, one spawn cron per workspace)
// can therefore pass the same file everywhere: workspace B's run never reads
// or overwrites workspace A's cursor. A shared single timestamp did exactly
// that and dropped B's changes that were older than A's last run.
//
// A file that still holds a bare timestamp (gc ≤ 0.2) is read as the cursor
// of whichever workspace asks. The first write turns it into the object with
// only the writing workspace's entry; another workspace then starts from the
// default window (last hour), with a note on stderr.

// readCursorFile returns the slot's cursor, or "" when the file or the slot's
// entry does not exist yet.
func readCursorFile(path, slot string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", clierr.New(fmt.Sprintf("Cannot read %s: %s", path, err))
	}
	raw := strings.TrimSpace(string(b))
	if !strings.HasPrefix(raw, "{") {
		return raw, nil // bare timestamp from gc ≤ 0.2 — validated by the caller
	}
	m, err := parseCursorMap(path, raw)
	if err != nil {
		return "", err
	}
	return m[slot], nil
}

func parseCursorMap(path, raw string) (map[string]string, error) {
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, clierr.New(
			fmt.Sprintf("Cursor file %s is not a cursor object (\"%s\").", path, output.OneLine(raw, 40)),
			"Pass --since to reset it.")
	}
	return m, nil
}

// writeCursorFile sets the slot's entry and keeps every other workspace's entry.
func writeCursorFile(path, slot, cursor string) error {
	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		raw := strings.TrimSpace(string(b))
		if strings.HasPrefix(raw, "{") {
			if m, err = parseCursorMap(path, raw); err != nil {
				return err
			}
		}
		// A bare timestamp was this workspace's cursor (see readCursorFile) and
		// is replaced by the entry below.
	} else if !errors.Is(err, fs.ErrNotExist) {
		return clierr.New(fmt.Sprintf("Cannot read %s: %s", path, err))
	}
	m[slot] = cursor
	b, err := json.MarshalIndent(m, "", "  ") // map keys come out sorted: stable diffs
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return config.AtomicWrite(path, string(b)+"\n", 0o600)
}
