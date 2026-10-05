// gc — the GROUNDCONTROL command line for agents.
package main

import (
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/cli"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/clierr"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/config"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	env := config.Env{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	sys := &cli.Sys{
		Env:          env,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		StdinIsTTY:   term.IsTerminal(int(os.Stdin.Fd())),
		PromptHidden: promptHidden,
		OnSignal:     onSignal,
		Version:      version,
	}
	os.Exit(cli.Run(os.Args[1:], sys))
}

// promptHidden reads a line from the TTY without echoing it (for the API key).
// Raw mode, so Ctrl-C arrives as a byte and the terminal is always restored.
func promptHidden(question string) (string, error) {
	io.WriteString(os.Stderr, question)
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	restore := func() {
		term.Restore(fd, state)
		io.WriteString(os.Stderr, "\n")
	}
	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(b)
		if err != nil || n == 0 {
			restore()
			return string(buf), nil
		}
		switch ch := b[0]; {
		case ch == '\r' || ch == '\n' || ch == 0x04:
			restore()
			return string(buf), nil
		case ch == 0x03:
			restore()
			return "", &clierr.Error{Msg: "Aborted.", Code: 130}
		case ch == 0x7f || ch == '\b':
			if len(buf) > 0 {
				_, size := utf8.DecodeLastRune(buf)
				buf = buf[:len(buf)-size]
			}
		case ch >= ' ':
			buf = append(buf, ch)
		}
	}
}

// onSignal subscribes to SIGINT/SIGTERM; the returned func unsubscribes.
func onSignal(handler func(signal string)) func() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-ch:
				name := "SIGINT"
				if s == syscall.SIGTERM {
					name = "SIGTERM"
				}
				handler(name)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
