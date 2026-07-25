# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Text-to-Speech MCP server plugin for Claude Code written in Go. It converts text to speech using OpenAI's TTS API, ElevenLabs, or Kokoro (a free, open-weight model run through a local server) and plays audio via platform-native players.

## Commands

```bash
# Build
make build              # Creates bin/tts-server

# Run locally (requires OPENAI_API_KEY)
make run

# Test
make test               # Run all tests
go test -v ./internal/server/...  # Run specific package tests

# Lint
make lint               # Runs golangci-lint (auto-installs if missing)

# Format
make fmt

# Install to Claude Code plugins
make install            # Installs to ~/.claude/plugins/claude-code-tts/
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  cmd/tts-server/main.go                                     │
│    Entry point - requires at least one usable provider:     │
│    an API key (OPENAI_API_KEY and/or ELEVENLABS_API_KEY),   │
│    or keyless Kokoro (TTS_PROVIDER=kokoro / KOKORO_BASE_URL) │
│                                                             │
│  internal/server/                                           │
│    server.go: MCP server setup, tool registration           │
│      - speak(text, provider, voice) → queues TTS job        │
│      - tts_status() → returns pool stats as JSON            │
│                                                             │
│    worker.go: Worker pool (2 workers, 50-slot queue)        │
│      - Holds a provider registry (name → tts.Synthesizer)   │
│      - Each Job records its provider and voice              │
│      - Concurrent job processing with goroutines            │
│      - Job history tracking (last 100 jobs)                 │
│      - Atomic counters for processed/failed stats           │
│                                                             │
│  internal/tts/                                              │
│    provider.go: Synthesizer interface + provider helpers    │
│      - Each provider validates its own voices               │
│      - DefaultProviderName(): TTS_PROVIDER env var, else    │
│        picked from configured API keys (OpenAI preferred).  │
│        Kokoro is never auto-picked (no key); opt-in only    │
│    openai.go: OpenAI TTS API client                         │
│      - POST /v1/audio/speech with tts-1 model               │
│      - Voices: alloy, echo, fable, onyx, nova, shimmer      │
│    elevenlabs.go: ElevenLabs TTS API client                 │
│      - POST /v1/text-to-speech/{voice_id}                   │
│      - Discovers account voices via GET /v1/voices (lazy,   │
│        cached); resolves names to IDs, raw IDs pass through │
│      - Free-tier safe: no hardcoded (deprecated) premade    │
│        voice IDs; falls back to Aria if discovery fails     │
│    kokoro.go: Kokoro TTS client (local, keyless)            │
│      - POST {KOKORO_BASE_URL}/v1/audio/speech, no auth      │
│      - OpenAI-compatible body; response_format=mp3          │
│      - Voice validation is pass-through (server decides)    │
│      - All clients return MP3 audio bytes                   │
│                                                             │
│  internal/audio/                                            │
│    player.go: Cross-platform audio playback                 │
│      - Mutex-protected (one audio at a time)                │
│      - macOS: afplay, Linux: mpv/ffplay/mpg123              │
│      - Windows: PowerShell Media.SoundPlayer                │
│                                                             │
│  cmd/tts-reader/main.go + internal/reader/                  │
│    Read-along view (launched by `tts-ctl read` / /tts-read) │
│      - transcript.go: parses the session's JSONL transcript │
│        into visible chat turns (skips tool calls, thinking, │
│        meta lines, sidechains, command noise)               │
│      - server.go: loopback-only HTTP server with embedded   │
│        web page (assets/), /api/tts synthesis endpoint,     │
│        SSE live updates as the transcript grows, and        │
│        single-instance reuse via /api/health + /api/load    │
│      - The BROWSER plays the audio here (not player.go) so  │
│        the page can highlight each sentence/word in sync;   │
│        sentence timing is exact (one clip per sentence),    │
│        word timing is estimated proportionally to length    │
│      - Inside VS Code the launcher does NOT open the system │
│        browser: it prints a clickable URL that opens as a   │
│        Simple Browser editor tab via the user's             │
│        workbench.externalUriOpeners rule (see README).      │
│        --browser forces the system browser anywhere         │
│                                                             │
│  scripts/tts-ctl `selection` (X11 only)                     │
│    Speaks the text currently highlighted in ANY window,     │
│    including the Claude Code chat panel, by reading the X11 │
│    primary selection via python3/tkinter. The chat panel is │
│    a closed webview (no extension API, no context-menu      │
│    injection), so highlight+hotkey is the only way to read  │
│    from it directly; README documents the VS Code           │
│    keybinding/task setup                                    │
└─────────────────────────────────────────────────────────────┘
```

## Key Design Decisions

- **Provider Interface**: `tts.Synthesizer` abstracts TTS providers; voice validation lives in each provider because voice names are provider-specific
- **Worker Pool Pattern**: Jobs are non-blocking; `speak()` returns immediately after queuing
- **Mutex-Protected Playback**: `audio.Player` ensures no overlapping audio
- **Job Queue**: Channel-based with 50 slots; returns error when full
- **MCP Protocol**: Uses `mcp-go` library for stdio-based communication with Claude Code

## Environment

- **Required**: at least one usable provider — an `OPENAI_API_KEY` or `ELEVENLABS_API_KEY`, or keyless Kokoro (set `TTS_PROVIDER=kokoro`, or `KOKORO_BASE_URL`)
- **Optional**: `TTS_PROVIDER` (`openai`, `elevenlabs`, or `kokoro`) to pick the default provider
- **Optional**: `KOKORO_BASE_URL` — root address of a local Kokoro-FastAPI server (default `http://localhost:8880`)
- **Optional**: `TTS_READER_PORT` — port for the read-along view's local server (default `8898`, loopback only)
- **Go Version**: 1.21+ (go.mod specifies 1.23)

## MCP Tools

| Tool | Parameters | Description |
|------|------------|-------------|
| `speak` | `text` (required), `provider` (optional: openai, elevenlabs, kokoro), `voice` (optional; OpenAI: alloy, echo, fable, onyx, nova, shimmer; ElevenLabs: a voice name from your account or a raw voice ID, default Aria; Kokoro: a voice name such as af_bella, default af_bella) | Queue TTS job |
| `tts_status` | none | Get queue/worker stats |
