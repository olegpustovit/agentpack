#!/bin/sh
set -eu
cd /demo
mkdir project
cd project
export AGENTPACK_HOME=/demo/state
export AGENTPACK_STAGING_ROOT=/demo/staging
export NO_COLOR=1
export TERM=dumb
printf '$ agentpack --version\n'
/agentpack --version
printf '\n$ agentpack init\n'
/agentpack --no-progress init
mkdir local-skill
cat > local-skill/SKILL.md <<'SKILL'
---
name: local-skill
description: Summarize a change and explain how to verify it.
---
Describe the change in plain language. Include the verification command.
SKILL
printf '\n$ agentpack add ./local-skill --no-sync\n'
/agentpack --no-progress add ./local-skill --no-sync
printf '\n$ agentpack sync\n'
/agentpack --no-progress sync
printf '\n$ agentpack sync --verify-only\n'
/agentpack --no-progress sync --verify-only
printf '\n$ check locked content hash\n'
grep '^content_hash' pack.lock
printf '\n$ check staged local skill\n'
find /demo/staging /tmp -type f -path '*/skills/local-skill/SKILL.md' | sort > /demo/staged-paths.txt
count=0
while IFS= read -r skill_file; do
  grep -q 'Describe the change in plain language. Include the verification command.' "$skill_file"
  count=$((count + 1))
done < /demo/staged-paths.txt
test "$count" -eq 6
for target in agy cursor grok opencode plugins; do
  grep -q "/$target/" /demo/staged-paths.txt
done
grep -q '/ap-c/' /demo/staged-paths.txt
printf 'PASS: six staged skill files contain the fixture instruction.\n'
printf 'PASS: Claude, Cursor, OpenCode, Codex, Grok and Antigravity paths exist.\n'
