# Claude sign-ins in the macOS keychain

Question: on a Mac, does a keychain sign-in hide the `~/.claude/.credentials.json` that agentpack
shares across the mesh? Agentpack never reads another program's keychain item (see
`internal/harness/claude/login.go`), and this check did not either.

## Observed (2026-11-26, Mac mini, macOS 27.0.1, over SSH, read-only)

| Command | Result |
|---|---|
| `ls -l ~/.claude/.credentials.json` | exists, mode `0600`, 1573 bytes |
| `security find-generic-password -s "Claude Code-credentials" >/dev/null 2>&1` | exit 0: the item exists |
| `security show-keychain-info` | exit 36, `User interaction is not allowed` |

No command used `-w` or `-g`, so no secret was read or printed.

## What this shows

- This Mac holds a `Claude Code-credentials` keychain item **and** the credential file.
- An SSH session can see that the item exists but cannot use the keychain, so SSH cannot tell
  whether the item and the file hold the same sign-in, or which one is newer.

## Not shown

- Which of the two a running Claude Code uses. Agentpack's code comment says Claude prefers the
  keychain and reads the file only when the keychain has no sign-in; this was **not verified** here.
- Whether the item is current, or left over from an old sign-in.
- Whether this ever caused a lost login.

If Claude does prefer the keychain, a sign-in shared to this Mac has no effect while the item
exists, and the Mac's own sign-in is renewed on its own, outside agentpack's single renewer. To
check, the owner would compare `claude auth status` in a Mac terminal with the account the other
machines use.
