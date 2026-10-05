// Package toolkit embeds the agent skills (skills/<name>/SKILL.md) into the
// gc binary. The .md files are canonical; `gc skills install` writes these
// compiled-in copies. It lives at the repo root because go:embed can't reach
// into a parent directory.
package toolkit

import "embed"

//go:embed skills/*/SKILL.md
var Skills embed.FS
