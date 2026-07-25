package reader

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// fakeSynth is a Synthesizer that returns fixed bytes without any network.
type fakeSynth struct {
	audio []byte
	fail  bool
}

func (f *fakeSynth) Name() string         { return "fake" }
func (f *fakeSynth) DefaultVoice() string { return "test-voice" }
func (f *fakeSynth) Voices() []string     { return []string{"test-voice", "other"} }
func (f *fakeSynth) IsValidVoice(v string) bool {
	return v == "test-voice" || v == "other"
}
func (f *fakeSynth) Synthesize(text, voice string) ([]byte, error) {
	if f.fail {
		return nil, errors.New("synthesis exploded")
	}
	return f.audio, nil
}

const miniTranscript = `{"type":"user","message":{"role":"user","content":"Hello."},"timestamp":"2026-07-25T10:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there."}]},"timestamp":"2026-07-25T10:00:02Z"}
`

func newTestReader(t *testing.T, synth tts.Synthesizer) (*httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(miniTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Options{
		Transcript:      path,
		Providers:       map[string]tts.Synthesizer{"fake": synth},
		DefaultProvider: "fake",
		Speed:           1.0,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, path
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d: %s", url, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesEndpoint(t *testing.T) {
	ts, path := newTestReader(t, &fakeSynth{audio: []byte("MP3")})
	var got struct {
		Transcript string    `json:"transcript"`
		Messages   []Message `json:"messages"`
	}
	getJSON(t, ts.URL+"/api/messages", &got)
	if got.Transcript != path {
		t.Errorf("transcript = %q, want %q", got.Transcript, path)
	}
	if len(got.Messages) != 2 || got.Messages[0].Text != "Hello." || got.Messages[1].Text != "Hi there." {
		t.Fatalf("unexpected messages: %+v", got.Messages)
	}
}

func TestConfigEndpoint(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{audio: []byte("MP3")})
	var got struct {
		DefaultProvider string                    `json:"default_provider"`
		Providers       map[string]providerConfig `json:"providers"`
		Speed           float64                   `json:"speed"`
	}
	getJSON(t, ts.URL+"/api/config", &got)
	if got.DefaultProvider != "fake" {
		t.Errorf("default_provider = %q", got.DefaultProvider)
	}
	if pc, ok := got.Providers["fake"]; !ok || pc.DefaultVoice != "test-voice" || len(pc.Voices) != 2 {
		t.Errorf("unexpected provider config: %+v", got.Providers)
	}
	if got.Speed != 1.0 {
		t.Errorf("speed = %v, want 1.0", got.Speed)
	}
}

func postTTS(t *testing.T, url string, body map[string]string) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url+"/api/tts", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestTTSEndpoint(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{audio: []byte("MP3-BYTES")})

	resp := postTTS(t, ts.URL, map[string]string{"text": "Hello world", "voice": "test-voice"})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "MP3-BYTES" {
		t.Errorf("body = %q", body)
	}

	if resp := postTTS(t, ts.URL, map[string]string{"text": ""}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty text: status %d, want 400", resp.StatusCode)
	}
	if resp := postTTS(t, ts.URL, map[string]string{"text": "hi", "provider": "nope"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown provider: status %d, want 400", resp.StatusCode)
	}
	if resp := postTTS(t, ts.URL, map[string]string{"text": "hi", "voice": "bogus"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid voice: status %d, want 400", resp.StatusCode)
	}
	if resp := postTTS(t, ts.URL, map[string]string{"text": strings.Repeat("a", maxTTSChars+1)}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized text: status %d, want 400", resp.StatusCode)
	}
}

func TestTTSEndpointSynthesisFailure(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{fail: true})
	resp := postTTS(t, ts.URL, map[string]string{"text": "hi"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
}

func TestGuardRejectsForeignOriginAndHost(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{audio: []byte("MP3")})

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/messages", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: status %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/messages", nil)
	req.Host = "evil.example" // DNS-rebinding shape: loopback IP, foreign Host
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign host: status %d, want 403", resp.StatusCode)
	}
}

func TestHealthAndLoad(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{audio: []byte("MP3")})

	var health struct {
		App string `json:"app"`
	}
	getJSON(t, ts.URL+"/api/health", &health)
	if health.App != healthApp {
		t.Fatalf("app = %q, want %q", health.App, healthApp)
	}

	other := filepath.Join(t.TempDir(), "other.jsonl")
	otherContent := `{"type":"user","message":{"role":"user","content":"Different session."}}` + "\n"
	if err := os.WriteFile(other, []byte(otherContent), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"path": other})
	resp, err := http.Post(ts.URL+"/api/load", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("load: status %d", resp.StatusCode)
	}

	var got struct {
		Transcript string    `json:"transcript"`
		Messages   []Message `json:"messages"`
	}
	getJSON(t, ts.URL+"/api/messages", &got)
	if got.Transcript != other {
		t.Errorf("transcript after load = %q, want %q", got.Transcript, other)
	}
	if len(got.Messages) != 1 || got.Messages[0].Text != "Different session." {
		t.Errorf("messages after load: %+v", got.Messages)
	}

	body, _ = json.Marshal(map[string]string{"path": filepath.Join(t.TempDir(), "missing.jsonl")})
	resp, err = http.Post(ts.URL+"/api/load", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("load missing file: status %d, want 400", resp.StatusCode)
	}
}

func TestStopEndpoint(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{audio: []byte("MP3")})
	resp, err := http.Post(ts.URL+"/api/stop", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop: status %d, want 200", resp.StatusCode)
	}
	getResp, err := http.Get(ts.URL + "/api/stop")
	if err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("stop via GET: status %d, want 405", getResp.StatusCode)
	}
}

func TestIndexServesPageWithCSP(t *testing.T) {
	ts, _ := newTestReader(t, &fakeSynth{audio: []byte("MP3")})
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("missing CSP header, got %q", csp)
	}
	page, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(page, []byte("Read along")) {
		t.Error("index page does not look like the read-along view")
	}
}
