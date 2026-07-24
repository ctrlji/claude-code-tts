---
description: Control the text-to-speech experience — mode, voice, read the last response, read a file, and more
argument-hint: mode full|sentence|off | last | file PATH | say TEXT | stop | speed RATE | voice NAME | code include|exclude | markdown keep|strip | cap N|none | show | reset
allowed-tools: Bash(~/.claude/plugins/claude-code-tts/bin/tts-ctl:*)
---

Run this exact command and nothing else:

```
~/.claude/plugins/claude-code-tts/bin/tts-ctl $ARGUMENTS
```

Then tell the user, in one short sentence, what changed or that audio is playing, based on the command's output. Do not take any other action.

For reference, the subcommands are:
- `mode full|sentence|off` — how much of each response is auto-spoken
- `last` (add `--with-code` or `--raw`) — read the last response aloud now
- `file PATH` (add `--with-code`) — read a text or Markdown file aloud now
- `say TEXT` — speak arbitrary text now
- `stop` — stop any read-out currently in progress
- `speed RATE` (0.5–2.0, or `default`) — set playback speed; pitch is preserved
- `voice NAME` (or `default`) — change the voice
- `code include|exclude` — read code blocks aloud, or skip them
- `markdown keep|strip` — keep Markdown markup, or strip it for cleaner speech
- `cap N|none` — limit spoken characters
- `show` — print the current settings
- `reset` — clear all on-the-fly overrides
