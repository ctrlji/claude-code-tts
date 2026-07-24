---
description: Read Claude's last response aloud (add --with-code to include code blocks, --raw to include everything)
argument-hint: [--with-code | --raw]
allowed-tools: Bash(~/.claude/plugins/claude-code-tts/bin/tts-ctl:*)
---

Run this exact command and nothing else:

```
~/.claude/plugins/claude-code-tts/bin/tts-ctl last $ARGUMENTS
```

Then tell the user in one short sentence that the last response is being read aloud. Do not take any other action.
