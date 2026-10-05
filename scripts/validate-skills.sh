#!/bin/sh
# Every skills/<name>/SKILL.md must open with frontmatter carrying `name`
# (equal to its directory) and a non-empty `description` — `npx skills add`
# and the agents' skill loaders skip a skill without them, silently.
set -eu
status=0
for f in skills/*/SKILL.md; do
  dir=$(basename "$(dirname "$f")")
  if [ "$(head -n 1 "$f")" != "---" ]; then echo "$f: no frontmatter"; status=1; continue; fi
  fm=$(awk 'NR==1{next} /^---$/{exit} {print}' "$f")
  name=$(printf '%s\n' "$fm" | sed -n 's/^name: *//p')
  desc=$(printf '%s\n' "$fm" | sed -n 's/^description: *//p')
  [ "$name" = "$dir" ] || { echo "$f: name '$name' != directory '$dir'"; status=1; }
  [ -n "$desc" ] || { echo "$f: empty description"; status=1; }
done
[ $status -eq 0 ] && echo "skills ok"
exit $status
