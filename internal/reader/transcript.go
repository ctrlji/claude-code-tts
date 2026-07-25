// Package reader serves the read-along view: a local web page that shows a
// Claude Code conversation and reads it aloud, keeping the sentence and word
// being spoken highlighted. Unlike the speak tool, the audio is played by the
// browser, not the native player, because only the page itself can keep a
// visual highlight in step with playback.
package reader

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Message is one visible chat turn extracted from a session transcript.
type Message struct {
	Role      string `json:"role"` // "user" or "assistant"
	Text      string `json:"text"` // Markdown-ish plain text of the turn
	Timestamp string `json:"timestamp,omitempty"`
}

// rawEntry mirrors just the fields we need from one transcript line. The
// transcript format is internal to Claude Code and can change between
// releases, so parsing is deliberately tolerant: a line that does not look
// like a chat turn is skipped, never treated as an error.
type rawEntry struct {
	Type        string `json:"type"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// contentBlock is one element of a structured message content array.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// systemReminderRe matches the <system-reminder> blocks Claude Code injects
// into user turns. They are context plumbing, not something the user typed,
// so the reader strips them out.
var systemReminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// noiseUserPrefixes mark user entries that are local-command plumbing (slash
// command echoes and their captured output), not real chat turns.
var noiseUserPrefixes = []string{
	"<command-name>",
	"<command-message>",
	"<local-command-stdout>",
	"<local-command-caveat>",
	"Caveat: The messages below were generated",
}

// ParseTranscript reads a Claude Code session transcript (a JSONL file) and
// returns the visible chat turns in order. Tool calls, tool results, thinking
// blocks, meta entries, and sidechain (subagent) entries are skipped. All text
// an assistant turn produces, including text between tool calls, is merged
// into a single message, because that is how the turn reads in the chat
// window.
func ParseTranscript(path string) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open transcript: %w", err)
	}
	defer f.Close()

	messages := []Message{}
	// A plain Reader, not a Scanner: single transcript lines can hold huge
	// tool results, far beyond any fixed Scanner buffer.
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			if msg, ok := parseLine(line); ok {
				if msg.Role == "assistant" && len(messages) > 0 && messages[len(messages)-1].Role == "assistant" {
					// Continuation of the same turn (tool results in between
					// were skipped): merge instead of starting a new bubble.
					messages[len(messages)-1].Text += "\n\n" + msg.Text
				} else {
					messages = append(messages, msg)
				}
			}
		}
		if err != nil {
			// io.EOF, or a truncated tail while the session is still being
			// written; either way the turns parsed so far are what we show.
			break
		}
	}
	return messages, nil
}

// parseLine converts one transcript line into a Message. ok is false for
// every line that is not a visible chat turn.
func parseLine(line string) (Message, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Message{}, false
	}
	var entry rawEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return Message{}, false
	}
	if entry.IsMeta || entry.IsSidechain {
		return Message{}, false
	}
	if entry.Type != "user" && entry.Type != "assistant" {
		return Message{}, false
	}

	text := extractText(entry.Message.Content)
	if entry.Type == "user" {
		text = systemReminderRe.ReplaceAllString(text, "")
		text = strings.TrimSpace(text)
		for _, p := range noiseUserPrefixes {
			if strings.HasPrefix(text, p) {
				return Message{}, false
			}
		}
	} else {
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return Message{}, false
	}
	return Message{Role: entry.Type, Text: text, Timestamp: entry.Timestamp}, true
}

// extractText pulls the plain text out of a message content field, which is
// either a bare string or an array of typed blocks. Only "text" blocks count:
// tool_use, tool_result, and thinking blocks are not part of the visible chat.
func extractText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// nonProjectChar matches every character Claude Code replaces with a dash
// when it turns a project path into a directory name under ~/.claude/projects.
var nonProjectChar = regexp.MustCompile(`[^A-Za-z0-9-]`)

// MungeProjectPath converts an absolute project path into the directory name
// Claude Code uses for it under ~/.claude/projects.
func MungeProjectPath(projectDir string) string {
	return nonProjectChar.ReplaceAllString(projectDir, "-")
}

// FindLatestTranscript returns the most recently modified session transcript
// for the given project directory. The newest file is almost always the
// session the user is sitting in, because Claude Code appends to it on every
// turn.
func FindLatestTranscript(projectDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return findLatestTranscriptIn(filepath.Join(home, ".claude", "projects"), projectDir)
}

// findLatestTranscriptIn is FindLatestTranscript with the projects root made
// explicit so tests can point it at a temporary directory.
func findLatestTranscriptIn(projectsRoot, projectDir string) (string, error) {
	dir := filepath.Join(projectsRoot, MungeProjectPath(projectDir))
	if _, err := os.Stat(dir); err != nil {
		// Some Claude Code versions munge only the path separators. Try that
		// spelling before giving up.
		alt := filepath.Join(projectsRoot, strings.ReplaceAll(projectDir, string(os.PathSeparator), "-"))
		if _, altErr := os.Stat(alt); altErr == nil {
			dir = alt
		} else {
			return "", fmt.Errorf("no Claude Code session directory for %s (looked in %s)", projectDir, dir)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var newest string
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); newest == "" || mod > newestMod {
			newest = filepath.Join(dir, e.Name())
			newestMod = mod
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no session transcripts (*.jsonl) in %s", dir)
	}
	return newest, nil
}
