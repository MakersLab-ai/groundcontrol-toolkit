package cli

import (
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/api"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

// Sys is everything a run touches outside the process: env, stdio, the TTY
// prompt and signals. main wires the real ones; tests inject their own.
type Sys struct {
	Env        config.Env
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	StdinIsTTY bool
	// PromptHidden reads a line from the TTY without echoing it (the API key).
	PromptHidden func(question string) (string, error)
	// OnSignal subscribes to SIGINT/SIGTERM (long-running commands); returns the unsubscribe.
	OnSignal func(handler func(signal string)) func()
	Version  string
}

func (s *Sys) readStdin() (string, error) {
	if s.Stdin == nil {
		return "", nil
	}
	b, err := io.ReadAll(s.Stdin)
	return string(b), err
}

type OptSpec struct {
	Name     string
	Bool     bool // false: takes a string value
	Short    string
	Multiple bool
	Value    string // placeholder in help, e.g. <id>
	Desc     string
}

type Command struct {
	Path     []string
	Summary  string
	Args     string // positional synopsis for help, e.g. `<task-id> [<body>|-]`
	MinArgs  int
	MaxArgs  int
	Options  []OptSpec
	Examples []string
	Details  string // long-form note printed under the usage line
	Run      func(*Ctx) error
}

func (c *Command) option(name string) (OptSpec, bool) {
	for _, o := range c.Options {
		if o.Name == name {
			return o, true
		}
	}
	return OptSpec{}, false
}

var GlobalOptions = []OptSpec{
	{Name: "json", Bool: true, Desc: "Print the raw API response as JSON"},
	{Name: "field", Value: "<path>", Desc: "Print one value, e.g. --field data.status (or just status)"},
	{Name: "profile", Value: "<name>", Desc: "Use this profile (default: GC_PROFILE, then the current one)"},
	{Name: "token", Value: "<key>", Desc: "API key for this call (overrides GC_API_KEY and the profile)"},
	{Name: "api-url", Value: "<url>", Desc: "API base URL (default: GC_API_URL, profile, production)"},
	{Name: "session-id", Value: "<id>", Desc: `Agent session id (GC_SESSION_ID) — shows the "working" badge`},
	{Name: "help", Bool: true, Short: "h", Desc: "Show help for this command"},
}

func globalOption(name string) (OptSpec, bool) {
	for _, o := range GlobalOptions {
		if o.Name == name {
			return o, true
		}
	}
	return OptSpec{}, false
}

// Values: string options hold a string, multiple ones []string, booleans true.
type Values map[string]any

type Ctx struct {
	Values  Values
	Args    []string
	Out     *output.Output
	Sys     *Sys
	Command *Command

	mu     sync.Mutex
	client *api.Client
}

func newCtx(cmd *Command, values Values, args []string, sys *Sys) *Ctx {
	c := &Ctx{Values: values, Args: args, Sys: sys, Command: cmd}
	field, _ := c.str("field")
	c.Out = &output.Output{JSON: c.bool("json"), Field: field, Stdout: sys.Stdout, Stderr: sys.Stderr}
	return c
}

// Auth resolves key/url/profile (lazily: only commands that talk to the API need it).
func (c *Ctx) Auth() (config.Resolved, error) {
	cfg, err := config.Load(c.Sys.Env)
	if err != nil {
		return config.Resolved{}, err
	}
	token, _ := c.str("token")
	profile, _ := c.str("profile")
	apiURL, _ := c.str("api-url")
	return config.Resolve(config.Flags{Token: token, Profile: profile, APIURL: apiURL}, c.Sys.Env, cfg)
}

func (c *Ctx) sessionID() string {
	if s, ok := c.str("session-id"); ok {
		return s
	}
	return c.Sys.Env["GC_SESSION_ID"]
}

// Client is the authenticated API client; no key → "not connected" (exit 3).
func (c *Ctx) Client() (*api.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	a, err := c.Auth()
	if err != nil {
		return nil, err
	}
	key, err := config.RequireKey(a)
	if err != nil {
		return nil, err
	}
	c.client = api.New(a.APIURL, key, c.sessionID())
	return c.client, nil
}

// PublicClient sends the key when there is one and works without (the guide).
func (c *Ctx) PublicClient() (*api.Client, error) {
	a, err := c.Auth()
	if err != nil {
		return nil, err
	}
	return api.New(a.APIURL, a.APIKey, c.sessionID()), nil
}

// ─── value helpers ────────────────────────────────────────────────────────────

// str is a string flag, or ok=false when absent or empty — empty optional flags are not sent.
func (c *Ctx) str(key string) (string, bool) {
	s, ok := c.Values[key].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// sv is str() as a JS value: the string, or Undefined.
func (c *Ctx) sv(key string) any {
	if s, ok := c.str(key); ok {
		return s
	}
	return js.Undefined
}

// given reports a string flag that was passed at all (even empty).
func (c *Ctx) given(key string) bool {
	_, ok := c.Values[key].(string)
	return ok
}

func (c *Ctx) bool(key string) bool {
	b, _ := c.Values[key].(bool)
	return b
}

func (c *Ctx) list(key string) []string {
	out := []string{}
	switch v := c.Values[key].(type) {
	case []string:
		for _, x := range v {
			if x != "" {
				out = append(out, x)
			}
		}
	case string:
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (c *Ctx) requireStr(key string) (string, error) {
	s, ok := c.str(key)
	if !ok {
		return "", clierr.Usage(fmt.Sprintf("Missing required flag --%s.", key))
	}
	return s, nil
}

// nullable: `none`/`null` → null (clears the field on PATCH), anything else passes through.
func nullable(v any) any {
	if v == "none" || v == "null" {
		return nil
	}
	return v
}

// num parses a numeric flag value (Undefined passes through).
func num(v any, flag string, allowNull bool) (any, error) {
	s, ok := v.(string)
	if !ok {
		return js.Undefined, nil
	}
	if allowNull && (s == "none" || s == "null") {
		return nil, nil
	}
	n := js.ParseNumber(s)
	if strings.TrimSpace(s) == "" || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, clierr.Usage(fmt.Sprintf("--%s must be a number, got \"%s\".", flag, s))
	}
	return n, nil
}

// readSource: `-` reads stdin, anything else is a file path.
func (c *Ctx) readSource(pathOrDash string) (string, error) {
	if pathOrDash == "-" {
		return c.Sys.readStdin()
	}
	b, err := os.ReadFile(pathOrDash)
	if err != nil {
		return "", clierr.New(fmt.Sprintf("Cannot read %s: %s", pathOrDash, err))
	}
	return string(b), nil
}

// textInput is text that may come inline (`--description "…"`), from a file
// (`--description-file notes.md`) or stdin (`--description-file -`).
// Returns Undefined when neither was given.
func (c *Ctx) textInput(inlineKey, fileKey string) (any, error) {
	file, hasFile := c.str(fileKey)
	if c.given(inlineKey) && hasFile {
		return nil, clierr.Usage(fmt.Sprintf("Use either --%s or --%s, not both.", inlineKey, fileKey))
	}
	if hasFile {
		s, err := c.readSource(file)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	return c.sv(inlineKey), nil
}

var trailingSpace = regexp.MustCompile(`\s+$`)

// bodyInput is a comment body: positional text, positional `-` (stdin), or --body-file.
func (c *Ctx) bodyInput(positional *string) (string, error) {
	file, hasFile := c.str("body-file")
	if positional != nil && hasFile {
		return "", clierr.Usage("Give the body either as an argument or with --body-file, not both.")
	}
	var body string
	var err error
	switch {
	case hasFile:
		body, err = c.readSource(file)
	case positional != nil && *positional == "-":
		body, err = c.Sys.readStdin()
	case positional != nil:
		body = *positional
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", clierr.Usage("The body is empty.", "Pass it as an argument, or Markdown via --body-file <path> / --body-file - (stdin).")
	}
	return strings.TrimRightFunc(body, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }), nil
}

func (c *Ctx) arg(i int) *string {
	if i < len(c.Args) {
		return &c.Args[i]
	}
	return nil
}

// jsonInput is JSON from `--data '<json>'`, `--data-file <path>` or `--data-file -`.
func (c *Ctx) jsonInput(inlineKey, fileKey string) (any, error) {
	raw, err := c.textInput(inlineKey, fileKey)
	if err != nil {
		return nil, err
	}
	s, ok := raw.(string)
	if !ok {
		return nil, clierr.Usage(fmt.Sprintf("Missing --%s '<json>' or --%s <path|->.", inlineKey, fileKey))
	}
	return parseJSON(s, fmt.Sprintf("--%s/--%s", inlineKey, fileKey))
}

func parseJSON(raw, what string) (any, error) {
	v, err := js.Parse([]byte(raw))
	if err != nil {
		return nil, clierr.Usage(fmt.Sprintf("%s is not valid JSON: %s", what, err))
	}
	return v, nil
}

// parallel runs the calls concurrently (Promise.all) and returns the first error in order.
func parallel(fns ...func() (any, error)) ([]any, error) {
	res := make([]any, len(fns))
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = f()
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return res, nil
}

// lines turns text into the []string Emit takes.
func lines(s ...string) []string { return s }

// keepNonEmpty is `.filter(Boolean)` over lines.
func keepNonEmpty(ls []string) []string {
	out := []string{}
	for _, l := range ls {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// queryEscape encodes like URLSearchParams.
func queryEscape(s string) string { return url.QueryEscape(s) }
