# Claude Code TTS Plugin

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/ybouhjira/claude-code-tts/actions/workflows/ci.yml/badge.svg)](https://github.com/ybouhjira/claude-code-tts/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/ybouhjira/claude-code-tts/branch/main/graph/badge.svg)](https://codecov.io/gh/ybouhjira/claude-code-tts)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green.svg)](https://modelcontextprotocol.io)

A Text-to-Speech MCP server plugin for Claude Code that converts text to speech using OpenAI's TTS API, ElevenLabs, or Kokoro (a free, open-weight model that runs on your own machine). Get audio feedback from Claude as you work!

![Demo](demo.gif)

## Features

- **Deterministic Auto-Speak**: Every Claude response is automatically spoken (via Stop hook)
- **Three TTS Providers**, selectable per request: OpenAI (alloy, echo, fable, onyx, nova, shimmer); ElevenLabs (any voice on your account, discovered automatically, plus raw voice IDs); and Kokoro (free and offline via a local server — no API key, no per-use cost)
- **Worker Pool Architecture**: Non-blocking queue with concurrent processing
- **Mutex-Protected Playback**: One audio plays at a time, no overlapping
- **Cross-Platform**: macOS (afplay), Linux (mpv/ffplay/mpg123), Windows (PowerShell)
- **Standalone CLI**: `speak-text` binary for direct TTS without MCP

## Quick Install

```bash
# One-liner installation
curl -fsSL https://raw.githubusercontent.com/ybouhjira/claude-code-tts/main/install.sh | bash
```

Or install manually:

```bash
git clone https://github.com/ybouhjira/claude-code-tts.git ~/.claude/plugins/claude-code-tts
cd ~/.claude/plugins/claude-code-tts
make install
```

## Requirements

- **Go 1.21+** (for building from source)
- **A TTS provider**, one of:
  - An **API key** for OpenAI (with TTS access) or ElevenLabs, or
  - A local **Kokoro** server — free and keyless (see [Free, offline TTS with Kokoro](#free-offline-tts-with-kokoro))
- **Audio Player**:
  - macOS: `afplay` (built-in)
  - Linux: `mpv`, `ffplay`, or `mpg123`
  - Windows: PowerShell (built-in)

## Configuration

Set an API key for at least one provider:

```bash
export OPENAI_API_KEY="sk-..."        # for the openai provider
export ELEVENLABS_API_KEY="..."       # for the elevenlabs provider
```

Or add them to your shell profile (`~/.zshrc` or `~/.bashrc`).

When a request does not name a provider, the server picks a default. The `TTS_PROVIDER` environment variable wins if set to `openai`, `elevenlabs`, or `kokoro`. Otherwise the default follows which API keys are configured, preferring OpenAI when both are set. Kokoro has no API key to detect, so it is never auto-selected — you opt into it explicitly, either with `TTS_PROVIDER=kokoro` or by naming it on a single request.

```bash
export TTS_PROVIDER="elevenlabs"      # optional: make ElevenLabs the default
```

To use the plugin in every project and every VS Code window at once — including making your keys reach the VS Code GUI, which does not inherit your shell environment — see [docs/global-setup.md](docs/global-setup.md).

### Free, offline TTS with Kokoro

Kokoro is an open-weight text-to-speech model. "Open-weight" means the trained model file is published (under the Apache-2.0 license) for anyone to run, so there is no hosted service and no API key. It runs on your own machine, on CPU or GPU, at no per-use cost.

Because the model is not a cloud endpoint, something has to run it locally. The simplest way is **Kokoro-FastAPI**, a small local server that wraps the model and exposes the *same* HTTP shape OpenAI's TTS API uses. This plugin's `kokoro` provider talks to that server, so once it is running you get free speech with no other setup.

**1. Start a local Kokoro server.** The one-line Docker option (CPU build):

```bash
docker run -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:latest
```

There is a GPU image (`kokoro-fastapi-gpu`) and a `pip`/`uv` install path too — see the [Kokoro-FastAPI project](https://github.com/remsky/Kokoro-FastAPI) for those and for the current list of voices.

**2. Point the plugin at it and select it.**

```bash
export TTS_PROVIDER="kokoro"                    # use Kokoro by default
export KOKORO_BASE_URL="http://localhost:8880"  # optional; this is the default
```

`KOKORO_BASE_URL` is the server's root address (host and port); the plugin appends the `/v1/audio/speech` path itself. Leave it unset to use `http://localhost:8880`.

**Voices.** Kokoro voice names look like `af_bella`, `af_heart`, `am_michael`, and `bf_emma`. The leading letters are a hint: `a` = American and `b` = British English, then `f` = female and `m` = male. The default is `af_bella`. The server also accepts weighted blends such as `af_bella(2)+af_sky(1)`. The plugin passes whatever voice you give straight through to the server, so any voice your server supports works; the server reports a clear error for an unknown one.

**Playback speed** is applied the same way as for the other providers, by the audio player from `TTS_SPEED` (see the controls below), so `speed` is not sent to the Kokoro server.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Claude Code                              │
│                         │                                    │
│                    MCP Protocol                              │
│                         │                                    │
│  ┌──────────────────────▼──────────────────────────────┐    │
│  │              TTS MCP Server (Go)                     │    │
│  │  ┌─────────────────────────────────────────────┐    │    │
│  │  │              Tool Handlers                   │    │    │
│  │  │  speak(text, provider, voice) │ tts_status() │    │    │
│  │  └─────────────┬─────────┴─────────────────────┘    │    │
│  │                │                                     │    │
│  │  ┌─────────────▼─────────────────────────────┐      │    │
│  │  │           Worker Pool (2 workers)          │      │    │
│  │  │  ┌─────────┐    ┌─────────────────────┐   │      │    │
│  │  │  │ Job     │───►│ Queue (50 slots)    │   │      │    │
│  │  │  │ Submit  │    └──────────┬──────────┘   │      │    │
│  │  │  └─────────┘               │              │      │    │
│  │  │                   ┌────────▼────────┐     │      │    │
│  │  │                   │ Worker 1 │ 2    │     │      │    │
│  │  │                   └────────┬────────┘     │      │    │
│  │  └────────────────────────────│──────────────┘      │    │
│  │                               │                      │    │
│  │  ┌────────────────────────────▼──────────────────┐  │    │
│  │  │            TTS Provider (per job)              │  │    │
│  │  │   OpenAI: POST /v1/audio/speech (tts-1)        │  │    │
│  │  │   ElevenLabs: POST /v1/text-to-speech/{voice}  │  │    │
│  │  │   Kokoro: POST /v1/audio/speech (local server) │  │    │
│  │  └───────────────────┬────────────────────────────┘  │    │
│  │                      │                               │    │
│  │  ┌───────────────────▼────────────────────────────┐  │    │
│  │  │         Audio Player (Mutex Protected)          │  │    │
│  │  │   macOS: afplay │ Linux: mpv │ Win: PowerShell  │  │    │
│  │  └─────────────────────────────────────────────────┘  │    │
│  └──────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

## Usage

### speak(text, provider, voice)

Convert text to speech and play it aloud.

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | Yes | Text to speak (max 4096 chars) |
| `provider` | string | No | `openai`, `elevenlabs`, or `kokoro` (default: see Configuration) |
| `voice` | string | No | Voice to use (default: the provider's default voice) |

**OpenAI Voices** (default: `alloy`):
| Voice | Description |
|-------|-------------|
| `alloy` | Neutral, balanced |
| `echo` | Male, warm |
| `fable` | British accent |
| `onyx` | Deep male |
| `nova` | Female, friendly |
| `shimmer` | Soft female |

**ElevenLabs Voices**

ElevenLabs does not use a fixed list of names. Instead, the plugin asks your account which voices it has (through the List voices endpoint, `GET /v1/voices`) and accepts any of those by name. This matters for free-tier keys: the old premade voices like "Rachel" were reclassified as Voice Library voices, which free keys cannot use through the API, so hardcoding them caused a `402 paid_plan_required` error. Using your account's own voices avoids that.

You can pass a voice in three ways:

- **A voice name from your account**, such as `Aria` or a custom voice you created. Names are case-insensitive.
- **A raw voice ID**, the 20-character identifier shown next to a voice in your ElevenLabs voice library (for example, Aria is `9BWtsMINqrJLrRacOk9x`). This always works, even without voice discovery.
- **Nothing at all.** The default is the first voice on your account, or Aria (`9BWtsMINqrJLrRacOk9x`, a current default voice usable on the free tier) if the account's voices cannot be read.

To see the exact voices and IDs your key can use, run:

```bash
curl -s -H "xi-api-key: $ELEVENLABS_API_KEY" https://api.elevenlabs.io/v1/voices \
  | grep -oE '"(name|voice_id)": *"[^"]*"'
```

**Kokoro Voices** (default: `af_bella`)

Kokoro voice names look like `af_bella`, `af_heart`, `am_michael`, and `bf_emma`. The plugin does not keep a fixed list: it passes whatever voice you give straight to your local Kokoro server, which is the final judge. That way new voices and weighted blends such as `af_bella(2)+af_sky(1)` work without a plugin update. See [Free, offline TTS with Kokoro](#free-offline-tts-with-kokoro) for setup, and list your server's voices with:

```bash
curl -s http://localhost:8880/v1/audio/voices
```

**Example:**
```
Use the speak tool to say "Build completed successfully!" with the nova voice.
Use the speak tool with the elevenlabs provider to say "Deploy finished" with the Aria voice.
```

### tts_status()

Get the current status of the TTS system.

**Returns:**
```json
{
  "worker_count": 2,
  "queue_size": 50,
  "queue_pending": 0,
  "total_processed": 15,
  "total_failed": 0,
  "is_playing": false,
  "providers": ["elevenlabs", "openai"],
  "recent_jobs": [...]
}
```

## Automatic TTS

This plugin includes a **Stop hook** that speaks each Claude response out loud. A "Stop hook" is a script that Claude Code runs every time a response finishes. The hook reads the response text, tidies it up, and plays it.

By default it speaks only the **first sentence**, as a short spoken summary. You can change how much it reads, whether it skips code, and which voice it uses. All of that is controlled by one config file.

The hook runs in the background, so it never blocks Claude's responses. It also saves each response to `last-response.md` inside the plugin folder, so you can replay the whole thing on demand (see `speak-last` below).

### Enabling auto-speak

The Stop hook only runs when Claude Code has loaded this project as a **plugin** — a bundle that Claude Code registers and reads a manifest from. Copying the files into `~/.claude/plugins/` is not enough on its own. Claude Code loads plugins from its own registry, not by scanning that folder, so a plain copy gives you the `speak` tool and the `/tts` commands but leaves the hook inactive. If the commands work but auto-speak stays silent, this is why.

There are two supported ways to turn the hook on.

**Option 1 — install it as a real plugin.** Add the plugin through Claude Code's `/plugin` menu, pointing at this repository, then restart Claude Code or run `/reload-plugins`. Claude Code then reads `.claude-plugin/plugin.json` and auto-discovers `hooks/hooks.json`, so the Stop hook loads the way it is meant to.

**Option 2 — register the hook yourself.** Add a `Stop` hook to your own user settings at `~/.claude/settings.json`, pointing at the installed script. Use this if you installed with `make install` or the `curl` one-liner instead of through the plugin menu:

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "$HOME/.claude/plugins/claude-code-tts/hooks/auto-speak.sh"
          }
        ]
      }
    ]
  }
}
```

After editing `settings.json`, restart Claude Code, or approve the new hook when it prompts you, so the change takes effect. If you already have other hooks, keep them: add `Stop` alongside them rather than replacing the whole `hooks` block.

### Controls

Put your settings in `~/.config/environment.d/claude-code-tts.conf`. Each setting is a plain `KEY=value` line. The hook re-reads this file on every response, so edits take effect immediately — no restart or re-login needed. A value you export in your shell, or put in front of a single command, always overrides the file.

| Setting | Values | Default | What it does |
|---------|--------|---------|--------------|
| `TTS_SPEAK_MODE` | `sentence`, `full`, `off` | `sentence` | How much of each response to speak. `off` stops auto-speak but keeps the on-demand commands. |
| `TTS_MAX_CHARS` | a number; `0` = no cap | `200` in sentence mode, `0` in full mode | Upper limit on the number of spoken characters. |
| `TTS_STRIP_CODE` | `1`, `0` | `1` | `1` skips fenced code blocks; `0` reads them aloud. |
| `TTS_STRIP_MARKDOWN` | `1`, `0` | `1` | `1` removes Markdown markup (headings, links, emphasis) for cleaner speech. |
| `TTS_PROVIDER` | `openai`, `elevenlabs`, `kokoro` | your `.env` value | Which provider to use. |
| `TTS_VOICE` | a provider voice name or ID | provider default | Which voice to use. |
| `KOKORO_BASE_URL` | a URL | `http://localhost:8880` | Root address of your local Kokoro server (used only when the provider is `kokoro`). |
| `TTS_CHUNK_CHARS` | a number | `1500` | Max characters per request when reading long text; longer input is split on sentence boundaries. |

A starter file with all of these documented lives at `config/claude-code-tts.conf.example`.

Some common setups:

```bash
# Read the whole response, but skip code blocks (good for hands-free review)
TTS_SPEAK_MODE=full
TTS_STRIP_CODE=1

# Read everything, including code
TTS_SPEAK_MODE=full
TTS_STRIP_CODE=0

# Turn auto-speak off and use the on-demand commands instead
TTS_SPEAK_MODE=off
```

### Change it on the fly (slash commands)

Editing the config file is fine for your defaults, but for quick changes during a session use the slash commands. They change settings instantly. The change applies to the very next response, and it overrides the config file until you reset it.

| Command | What it does |
|---------|--------------|
| `/tts-full` | Speak the whole of every response from now on |
| `/tts-sentence` | Go back to speaking only the first sentence |
| `/tts-off` | Stop automatic speaking |
| `/tts-last` | Read the last response aloud now (add `--with-code` to include code) |
| `/tts mode full\|sentence\|off` | Set the auto-speak mode |
| `/tts file PATH` | Read a text or Markdown file aloud now |
| `/tts say TEXT` | Speak some text right now |
| `/tts stop` | Stop any read-out that is currently playing |
| `/tts speed RATE` | Set playback speed, `0.5`–`2.0` (`default` for normal); pitch is preserved |
| `/tts voice NAME` | Change the voice (or `default` to clear it) |
| `/tts code include\|exclude` | Read code blocks aloud, or skip them |
| `/tts markdown keep\|strip` | Keep Markdown markup, or strip it for cleaner speech |
| `/tts cap N\|none` | Limit the number of spoken characters |
| `/tts show` | Print the current settings |
| `/tts reset` | Clear the on-the-fly changes and use the config file again |

Under the hood these run `tts-ctl`, a small command installed next to `speak-text`. You can run it directly in a terminal too, for example `tts-ctl mode full` or `tts-ctl show`.

Two notes. A brand-new slash command may only appear after you restart Claude Code once. The on-the-fly settings are stored in a `runtime.env` file inside the plugin folder, and `/tts reset` deletes it.

### speak-text CLI

A standalone binary for direct TTS without going through MCP:

```bash
# Basic usage
speak-text "Hello world"

# With voice selection
speak-text -voice onyx "Error occurred"

# With the ElevenLabs provider (uses your account's default voice)
speak-text -provider elevenlabs "Error occurred"

# ...or name a voice / pass a raw voice ID
speak-text -provider elevenlabs -voice Aria "Error occurred"
speak-text -provider elevenlabs -voice 9BWtsMINqrJLrRacOk9x "Error occurred"

# With the free, local Kokoro provider (needs a running Kokoro server; no API key)
speak-text -provider kokoro "Build finished"
speak-text -provider kokoro -voice am_michael "Build finished"
```

Located at `~/.claude/plugins/claude-code-tts/bin/speak-text` after installation.

### speak-last — replay the last response

Speaks the most recent Claude response that the hook cached. By default it reads the whole response and skips code.

```bash
speak-last                 # whole last response, no code
speak-last --with-code     # include code blocks
speak-last --raw           # include code and Markdown markup, unchanged
speak-last --voice onyx    # override the voice
speak-last --provider openai
```

### speak-file — read a document aloud

Reads any text or Markdown file out loud. By default it strips code and Markdown so a document reads cleanly, and it splits long files into chunks so nothing is too large for the provider.

```bash
speak-file NOTES.md                # read the file, no code, no markup
speak-file --with-code README.md   # include code blocks
speak-file --raw CHANGELOG.md      # read it exactly as written
```

Both commands install to `~/.claude/plugins/claude-code-tts/bin/`, next to `speak-text`.

## Project Structure

```
claude-code-tts/
├── cmd/
│   ├── tts-server/
│   │   └── main.go           # MCP server entry point
│   └── speak-text/
│       └── main.go           # Standalone CLI binary
├── hooks/
│   ├── hooks.json            # Plugin hook declaration (Stop → auto-speak.sh)
│   ├── auto-speak.sh         # Stop hook: speaks each response per config
│   └── tts-common.sh         # Shared helpers: config, text cleaning, chunking
├── scripts/
│   ├── speak-last            # Replay the last response on demand
│   ├── speak-file            # Read a text/Markdown file aloud
│   └── tts-ctl               # Change settings on the fly (backs the /tts commands)
├── commands/
│   ├── tts.md                # /tts dispatcher slash command
│   ├── tts-last.md           # /tts-last, and tts-full/sentence/off shortcuts
│   └── ...
├── config/
│   └── claude-code-tts.conf.example  # Documented config with all the knobs
├── internal/
│   ├── audio/
│   │   └── player.go         # Cross-platform audio playback
│   ├── server/
│   │   ├── server.go         # MCP server & tool handlers
│   │   └── worker.go         # Worker pool implementation
│   └── tts/
│       ├── provider.go       # Synthesizer interface + provider registry
│       ├── openai.go         # OpenAI TTS client
│       ├── elevenlabs.go     # ElevenLabs TTS client
│       └── kokoro.go         # Kokoro TTS client (local, keyless)
├── .claude-plugin/
│   └── plugin.json            # Plugin manifest (name, version, metadata)
├── Makefile                   # Build automation
└── install.sh                 # One-liner installer
```

## Building from Source

```bash
# Clone the repository
git clone https://github.com/ybouhjira/claude-code-tts.git
cd claude-code-tts

# Build
make build

# Install to Claude Code plugins
make install

# Run tests
make test
```

## Troubleshooting

### "at least one API key is required"
The server needs an API key for at least one provider:
```bash
export OPENAI_API_KEY="sk-..."
# and/or
export ELEVENLABS_API_KEY="..."
```

### "No suitable audio player found on Linux"
Install one of: `mpv`, `ffplay`, or `mpg123`:
```bash
# Ubuntu/Debian
sudo apt install mpv

# Fedora
sudo dnf install mpv

# Arch
sudo pacman -S mpv
```

### Audio not playing on macOS
Check that `afplay` works:
```bash
# Test with a sample audio file
afplay /System/Library/Sounds/Ping.aiff
```

### Queue is full
The default queue size is 50. If you're hitting this limit:
1. Wait for current jobs to complete
2. Check `tts_status()` to see pending jobs
3. The queue will drain as jobs are processed

### High latency
- OpenAI TTS API typically takes 1-3 seconds per request
- Audio files must download completely before playing
- Consider keeping messages short for faster feedback

## API Costs

With the OpenAI provider, the plugin uses the `tts-1` model:
- **Cost**: ~$0.015 per 1,000 characters
- **Example**: "Hello, world!" (13 chars) = ~$0.0002

With the ElevenLabs provider, the plugin uses the `eleven_multilingual_v2` model. ElevenLabs bills characters against your plan's monthly quota rather than per request, so cost depends on your subscription tier.

With the Kokoro provider there is **no cost**: the model runs on your own machine, so you pay nothing per request and need no API key. The only trade-off is that you run a small local server, and synthesis speed depends on your hardware (a GPU is much faster than CPU).

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Credits

- [OpenAI TTS API](https://platform.openai.com/docs/guides/text-to-speech)
- [ElevenLabs TTS API](https://elevenlabs.io/docs/api-reference/text-to-speech)
- [Kokoro-82M](https://huggingface.co/hexgrad/Kokoro-82M) - the open-weight TTS model
- [Kokoro-FastAPI](https://github.com/remsky/Kokoro-FastAPI) - local OpenAI-compatible Kokoro server
- [mcp-go](https://github.com/mark3labs/mcp-go) - Go MCP implementation
- [Model Context Protocol](https://modelcontextprotocol.io)
