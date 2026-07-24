---
description: Control the text-to-speech experience — mode, voice, read the last response, read a file, and more
argument-hint: mode full|sentence|off | last | file PATH | say TEXT | code include|exclude | voice NAME | cap N|none | show | reset
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
- `code include|exclude` — read code blocks aloud, or skip them
- `voice NAME` (or `default`) — change the voice
- `cap N|none` — limit spoken characters
- `show` — print the current settings
- `reset` — clear all on-the-fly overrides
