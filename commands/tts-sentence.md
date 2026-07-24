---
description: Auto-speak only the first sentence of each Claude response
allowed-tools: Bash(~/.claude/plugins/claude-code-tts/bin/tts-ctl:*)
---

Run this exact command and nothing else:

```
~/.claude/plugins/claude-code-tts/bin/tts-ctl mode sentence
```

Then tell the user in one short sentence that only the first sentence will be spoken. Do not take any other action.
