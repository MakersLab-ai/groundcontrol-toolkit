package cli

import (
	"net/url"

	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/js"
	"github.com/MakersLab-ai/groundcontrol-toolkit/internal/output"
)

func guideCommands() []*Command {
	return []*Command{
		{
			Path:     []string{"guide"},
			Summary:  "The agent guide, served by GROUNDCONTROL (always current): list topics, or print one",
			Args:     "[<topic>]",
			MinArgs:  0,
			MaxArgs:  1,
			Details:  "Topics include start, tasks, docs, datasheets, goals, coding. The body is Markdown, printed raw.",
			Examples: []string{"gc guide", "gc guide tasks", "gc guide datasheets"},
			Run: func(c *Ctx) error {
				// Public: the key is sent when there is one, and it works without.
				client, err := c.PublicClient()
				if err != nil {
					return err
				}
				if len(c.Args) == 0 || c.Args[0] == "" {
					res, err := client.Get("/guide")
					if err != nil {
						return err
					}
					return c.Out.Emit(res, func() []string {
						rows := js.List(js.Get(res, "data"))
						width := 0
						for _, r := range rows {
							width = max(width, len([]rune(js.Str(js.Get(r, "topic")))))
						}
						ls := []string{}
						for _, r := range rows {
							summary := ""
							if s := js.Get(r, "summary"); js.Truthy(s) {
								summary = " — " + js.Str(s)
							}
							ls = append(ls, output.PadEnd(js.Str(js.Get(r, "topic")), width)+"  "+js.Str(js.Get(r, "title"))+summary)
						}
						return append(ls, "", "Read one: gc guide <topic>")
					})
				}
				res, err := client.Get("/guide/" + url.PathEscape(c.Args[0]))
				if err != nil {
					return err
				}
				return c.Out.Emit(res, func() []string { return lines(output.TrimEnd(js.S(js.Get(res, "data", "body")))) })
			},
		},
	}
}
