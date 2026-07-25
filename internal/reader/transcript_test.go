package reader

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixture covers every line shape the parser must handle: summaries, meta
// entries, plain and structured content, thinking-only lines, tool results,
// slash-command noise, system-reminder plumbing, sidechains, and garbage.
const fixture = `{"type":"summary","summary":"Old chat"}
{"type":"user","isMeta":true,"message":{"role":"user","content":"meta noise"}}
{"type":"user","message":{"role":"user","content":"Hello there."},"timestamp":"2026-07-25T10:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"}]},"timestamp":"2026-07-25T10:00:05Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi! How can I help?"}]},"timestamp":"2026-07-25T10:00:06Z"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"tool output"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Second part."}]},"timestamp":"2026-07-25T10:00:08Z"}
{"type":"user","message":{"role":"user","content":"<command-name>/model</command-name>"}}
{"type":"user","message":{"role":"user","content":"<local-command-stdout>ok</local-command-stdout>"}}
{"type":"user","message":{"role":"user","content":"Thanks!<system-reminder>context plumbing</system-reminder>"},"timestamp":"2026-07-25T10:01:00Z"}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent text"}]}}
this line is not JSON at all
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"You are welcome."}]},"timestamp":"2026-07-25T10:01:05Z"}
`

func writeTranscript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseTranscript(t *testing.T) {
	msgs, err := ParseTranscript(writeTranscript(t, fixture))
	if err != nil {
		t.Fatalf("ParseTranscript: %v", err)
	}

	want := []Message{
		{Role: "user", Text: "Hello there."},
		{Role: "assistant", Text: "Hi! How can I help?\n\nSecond part."},
		{Role: "user", Text: "Thanks!"},
		{Role: "assistant", Text: "You are welcome."},
	}
	if len(msgs) != len(want) {
		t.Fatalf("got %d messages, want %d: %+v", len(msgs), len(want), msgs)
	}
	for i, w := range want {
		if msgs[i].Role != w.Role {
			t.Errorf("message %d: role %q, want %q", i, msgs[i].Role, w.Role)
		}
		if msgs[i].Text != w.Text {
			t.Errorf("message %d: text %q, want %q", i, msgs[i].Text, w.Text)
		}
	}
	if msgs[0].Timestamp == "" {
		t.Error("expected the first user message to keep its timestamp")
	}
}

func TestParseTranscriptMissingFile(t *testing.T) {
	if _, err := ParseTranscript(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("expected an error for a missing transcript")
	}
}

func TestMungeProjectPath(t *testing.T) {
	got := MungeProjectPath("/home/a b/c.d_e")
	want := "-home-a-b-c-d-e"
	if got != want {
		t.Fatalf("MungeProjectPath = %q, want %q", got, want)
	}
}

func TestFindLatestTranscriptIn(t *testing.T) {
	root := t.TempDir()
	projectDir := "/home/user/my.project"
	dir := filepath.Join(root, MungeProjectPath(projectDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	older := filepath.Join(dir, "older.jsonl")
	newer := filepath.Join(dir, "newer.jsonl")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	got, err := findLatestTranscriptIn(root, projectDir)
	if err != nil {
		t.Fatalf("findLatestTranscriptIn: %v", err)
	}
	if got != newer {
		t.Fatalf("got %q, want %q", got, newer)
	}
}

func TestFindLatestTranscriptSlashOnlyFallback(t *testing.T) {
	root := t.TempDir()
	projectDir := "/home/user/under_score"
	// Only the older munging style (slashes replaced, underscore kept) exists.
	dir := filepath.Join(root, "-home-user-under_score")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(session, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findLatestTranscriptIn(root, projectDir)
	if err != nil {
		t.Fatalf("findLatestTranscriptIn: %v", err)
	}
	if got != session {
		t.Fatalf("got %q, want %q", got, session)
	}
}

func TestFindLatestTranscriptNoSessions(t *testing.T) {
	root := t.TempDir()
	projectDir := "/home/user/empty"
	if err := os.MkdirAll(filepath.Join(root, MungeProjectPath(projectDir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := findLatestTranscriptIn(root, projectDir); err == nil {
		t.Fatal("expected an error when the project has no transcripts")
	}
}
