// Package config stores the saved profiles (workspaces) in config.json and
// resolves which API key and URL a call uses.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
)

const DefaultAPIURL = "https://groundcontrol.makerslab.ai/api/v1"

type Env map[string]string

// Config is the parsed config.json. Profiles keep their file order and any
// fields a newer gc may have added; each profile is an object with api_url,
// api_key, workspace, agent, created_at.
type Config struct {
	Current  string // "" = none (null in the file)
	Profiles *js.Object
}

// Field reads a string field of a profile ("" when missing).
func Field(profile any, key string) string {
	v := js.Get(profile, key)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// NewProfile is the object onboarding stores.
func NewProfile(apiURL, apiKey, workspace, agent string) *js.Object {
	return js.O("api_url", apiURL, "api_key", apiKey, "workspace", workspace, "agent", agent, "created_at", js.NowISO())
}

// Dir is `$GC_CONFIG_DIR` > `$XDG_CONFIG_HOME/groundcontrol` > `~/.config/groundcontrol`.
func Dir(env Env) string {
	if env["GC_CONFIG_DIR"] != "" {
		return env["GC_CONFIG_DIR"]
	}
	if env["XDG_CONFIG_HOME"] != "" {
		return filepath.Join(env["XDG_CONFIG_HOME"], "groundcontrol")
	}
	return filepath.Join(Home(env), ".config", "groundcontrol")
}

// Home is $HOME, else the OS's idea of it.
func Home(env Env) string {
	if env["HOME"] != "" {
		return env["HOME"]
	}
	h, _ := os.UserHomeDir()
	return h
}

func Path(env Env) string { return filepath.Join(Dir(env), "config.json") }

func Load(env Env) (*Config, error) {
	path := Path(env)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Profiles: js.NewObject()}, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := js.Parse(data)
	if err != nil {
		return nil, clierr.New(fmt.Sprintf("Config file is not valid JSON: %s. Fix or delete it, then run gc onboarding again.", path))
	}
	cfg := &Config{Profiles: js.NewObject()}
	if s, ok := js.Get(parsed, "current").(string); ok {
		cfg.Current = s
	}
	if p := js.Obj(js.Get(parsed, "profiles")); p != nil {
		cfg.Profiles = p
	}
	return cfg, nil
}

// Profile returns the named profile, or nil.
func (c *Config) Profile(name string) any {
	if name == "" {
		return nil
	}
	v, ok := c.Profiles.Get(name)
	if !ok {
		return nil
	}
	return v
}

// Save writes the config the only way a file holding API keys should be
// written: directory 0700, file 0600 from the first byte (the tmp file is
// created with that mode), and an atomic rename so a crash or a concurrent
// reader never sees half a file.
func Save(cfg *Config, env Env) (string, error) {
	dir := Dir(env)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	var current any
	if cfg.Current != "" {
		current = cfg.Current
	}
	path := Path(env)
	content := js.StringifyIndent(js.O("current", current, "profiles", cfg.Profiles)) + "\n"
	return path, AtomicWrite(path, content, 0o600)
}

// AtomicWrite writes a sibling tmp file (exclusive, with `mode`) and renames it.
func AtomicWrite(path, content string, mode os.FileMode) error {
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	fail := func(e error) error {
		f.Close()
		os.Remove(tmp)
		return e
	}
	if _, err := f.WriteString(content); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil { // umask can strip bits from `mode`; make it exact
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RedactKey shows `gc_live_…abcd` — never more than the last four characters.
func RedactKey(key string) string {
	if key == "" {
		return "(none)"
	}
	prefix := ""
	if strings.HasPrefix(key, "gc_live_") {
		prefix = "gc_live_"
	}
	tail := ""
	if len(key) > 12 {
		tail = key[len(key)-4:]
	}
	return prefix + "…" + tail
}

// Slugify names a profile after its workspace: accents folded, lower-case,
// [a-z0-9] runs joined by "-", at most 40 characters, "default" when empty.
func Slugify(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 0x300 && r <= 0x36f { // combining marks
			continue
		}
		if f, ok := fold(r); ok {
			b.WriteString(f)
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	lower := strings.ToLower(b.String())
	var out strings.Builder
	dash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			dash = false
		} else if !dash {
			out.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(out.String(), "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if slug == "" {
		return "default"
	}
	return slug
}

// Flags are the global flags that select a key/URL/profile.
type Flags struct {
	Token   string
	Profile string
	APIURL  string
}

type Resolved struct {
	APIURL  string
	APIKey  string
	Profile string // the profile that supplied the key/url, if one was selected
	// KeySource: flag | env | profile | none
	KeySource string
}

// Resolve: key `--token` > `GC_API_KEY` > profile. URL: `--api-url` >
// `GC_API_URL` > profile > default. Profile: `--profile` > `GC_PROFILE` >
// `current`. A profile named explicitly that doesn't exist is an error —
// silently falling back to another workspace would write into the wrong board.
func Resolve(flags Flags, env Env, cfg *Config) (Resolved, error) {
	explicit := flags.Profile
	if explicit == "" {
		explicit = env["GC_PROFILE"]
	}
	name := explicit
	if name == "" {
		name = cfg.Current
	}
	profile := cfg.Profile(name)
	if explicit != "" && profile == nil {
		known := cfg.Profiles.Keys()
		msg := fmt.Sprintf("Unknown profile \"%s\".", explicit)
		if len(known) > 0 {
			msg += " Known: " + strings.Join(known, ", ") + "."
		} else {
			msg += " No profiles yet — run gc onboarding."
		}
		return Resolved{}, clierr.New(msg)
	}
	r := Resolved{KeySource: "none"}
	switch {
	case flags.Token != "":
		r.APIKey, r.KeySource = flags.Token, "flag"
	case env["GC_API_KEY"] != "":
		r.APIKey, r.KeySource = env["GC_API_KEY"], "env"
	case Field(profile, "api_key") != "":
		r.APIKey, r.KeySource = Field(profile, "api_key"), "profile"
	}
	url := flags.APIURL
	if url == "" {
		url = env["GC_API_URL"]
	}
	if url == "" {
		url = Field(profile, "api_url")
	}
	if url == "" {
		url = DefaultAPIURL
	}
	r.APIURL = strings.TrimRight(url, "/")
	if profile != nil {
		r.Profile = name
	}
	return r, nil
}

// RequireKey: no key is "not connected" (exit 3).
func RequireKey(r Resolved) (string, error) {
	if r.APIKey == "" {
		return "", clierr.NotConnected("no API key", r.APIURL)
	}
	return r.APIKey, nil
}
