// Package clierr holds the CLI's error kinds and exit codes.
//
// Exit codes: 0 ok · 1 API/other failure · 2 usage mistake (unknown flag,
// missing argument) · 3 not connected (no API key, or the key was rejected).
// A caller — usually an agent — can tell "I called it wrong" from "the call
// failed" from "this machine isn't set up", and only the last one needs a human.
package clierr

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

type Error struct {
	Msg  string
	Hint string
	Code int
}

func (e *Error) Error() string { return e.Msg }

// New is a failure (exit 1), with an optional hint line.
func New(msg string, hint ...string) *Error {
	return &Error{Msg: msg, Hint: strings.Join(hint, ""), Code: 1}
}

// Usage is a usage mistake (exit 2).
func Usage(msg string, hint ...string) *Error {
	return &Error{Msg: msg, Hint: strings.Join(hint, ""), Code: 2}
}

const DefaultOrigin = "https://groundcontrol.makerslab.ai"

// Rejected is the not-connected reason for a 401.
const Rejected = "the API key was rejected: 401"

// OriginOf is new URL(apiUrl).origin, or production when it isn't a URL.
func OriginOf(apiURL string) string {
	if apiURL == "" {
		return DefaultOrigin
	}
	u, err := url.Parse(apiURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return DefaultOrigin
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// NotConnectedText is the way back for an agent that finds gc unconnected:
// written for the agent to relay to its human.
func NotConnectedText(reason, apiURL string) string {
	origin := OriginOf(apiURL)
	return strings.Join([]string{
		"GROUNDCONTROL is not connected on this machine (" + reason + ").",
		"To connect:",
		"  1. Your human registers at " + origin + " (or opens Settings → API Keys there if the workspace exists).",
		"  2. After registration the setup screen (/agent ready_) shows a prompt with the key; or create a key under Settings → API Keys.",
		`  3. Run: gc onboarding --token "gc_live_…"`,
		"Then check with: gc context",
	}, "\n")
}

// NotConnected exits 3 with the connect directions.
func NotConnected(reason, apiURL string) *Error {
	return &Error{Msg: NotConnectedText(reason, apiURL), Code: 3}
}

var unauthorizedRe = regexp.MustCompile(`API error 401\b`)

// IsUnauthorized: the API client throws `GROUNDCONTROL API error <status>: …`.
func IsUnauthorized(err error) bool {
	return err != nil && unauthorizedRe.MatchString(err.Error())
}

// Code is the exit code an error maps to.
func Code(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 1
}
