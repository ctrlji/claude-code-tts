package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// KokoroClient implements StreamSynthesizer so callers can start playback as
// audio arrives.
var _ StreamSynthesizer = (*KokoroClient)(nil)

// defaultKokoroBaseURL is the address Kokoro-FastAPI listens on out of the box.
// KOKORO_BASE_URL overrides it. The value is the server root (host and port);
// the OpenAI-compatible speech path is appended to it.
const defaultKokoroBaseURL = "http://localhost:8880"

// kokoroSpeechPath is the OpenAI-compatible endpoint Kokoro-FastAPI exposes.
const kokoroSpeechPath = "/v1/audio/speech"

// kokoroVoicesPath is the endpoint that lists the voices the running server has
// loaded. Asking the server is the only way to get an accurate list, because
// which voices exist depends on the image and on the voice files it was given.
const kokoroVoicesPath = "/v1/audio/voices"

// kokoroDiscoveryTimeout bounds a voice-list request. The server is local, so a
// slow reply means something is wrong and the built-in list should be used
// instead of making the caller wait.
const kokoroDiscoveryTimeout = 5 * time.Second

// kokoroDiscoveryRetry is how long to wait before asking again after a failed
// attempt. Kokoro runs on the user's own machine and is often started after
// this process, so one failure must not disable the voice list for good.
const kokoroDiscoveryRetry = 30 * time.Second

// defaultKokoroVoice is the voice used when none is given. af_bella has shipped
// with Kokoro-FastAPI since its earliest releases, so it is a safe default.
const defaultKokoroVoice = "af_bella"

// kokoroKnownVoices is a short, well-known subset of Kokoro's English voices,
// used only for help text. It is intentionally not exhaustive: Kokoro's voice
// list keeps growing and supports weighted blends such as "af_bella(2)+af_sky(1)",
// so IsValidVoice accepts any non-empty string and lets the server be the judge.
var kokoroKnownVoices = []string{
	"af_bella", "af_heart", "af_sky", "af_sarah", "af_nicole",
	"am_michael", "am_adam", "bf_emma", "bf_isabella", "bm_george",
}

// KokoroClient synthesizes speech through a local Kokoro-FastAPI server.
// Kokoro is an open-weight model that runs on your own machine, so this client
// needs no API key. It talks to the server's OpenAI-compatible speech endpoint,
// which accepts the same request shape as OpenAI's TTS API.
type KokoroClient struct {
	httpClient *http.Client
	model      string
	baseURL    string // full speech endpoint, e.g. http://localhost:8880/v1/audio/speech
	voicesURL  string // full voice-list endpoint, e.g. http://localhost:8880/v1/audio/voices

	// The voice list the running server reports, fetched on first use and then
	// cached. kokoroKnownVoices is only the fallback for when the server cannot
	// be reached, so these fields are what make the real list available.
	mu          sync.Mutex
	discovered  []string
	lastAttempt time.Time
	attempted   bool
}

// NewKokoroClient creates a new Kokoro TTS client. The server address comes from
// KOKORO_BASE_URL (the server root) and falls back to http://localhost:8880.
func NewKokoroClient() *KokoroClient {
	root := strings.TrimSpace(os.Getenv("KOKORO_BASE_URL"))
	if root == "" {
		root = defaultKokoroBaseURL
	}
	return &KokoroClient{
		httpClient: &http.Client{
			// Deliberately no overall Timeout: when streaming, the response body
			// is read at playback speed, so a long read-out can take longer than
			// any fixed timeout and would be cut off mid-sentence. Instead, bound
			// only the parts that should be quick — establishing the connection
			// and receiving the response headers — so a missing or dead server
			// still fails fast.
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
		model:     "kokoro",
		baseURL:   kokoroEndpoint(root),
		voicesURL: kokoroVoicesEndpoint(root),
	}
}

// kokoroEndpoint turns a server root into the full speech-endpoint URL. It is
// forgiving: a trailing slash is trimmed, and a root that already includes the
// speech path is used as-is so KOKORO_BASE_URL can be either form.
func kokoroEndpoint(root string) string {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	if strings.HasSuffix(root, kokoroSpeechPath) {
		return root
	}
	return root + kokoroSpeechPath
}

// kokoroVoicesEndpoint turns a server root into the full voice-list URL. Like
// kokoroEndpoint it is forgiving, because KOKORO_BASE_URL is allowed to point
// either at the server root or straight at the speech endpoint.
func kokoroVoicesEndpoint(root string) string {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	root = strings.TrimSuffix(root, kokoroSpeechPath)
	return strings.TrimRight(root, "/") + kokoroVoicesPath
}

// Name returns the provider identifier.
func (c *KokoroClient) Name() string {
	return ProviderKokoro
}

// ensureVoicesLocked asks the server which voices it has, once, and caches the
// answer. The caller MUST hold c.mu. It never returns an error: when the server
// cannot be reached the cache stays empty and callers fall back to the built-in
// kokoroKnownVoices list. A failed attempt is retried after kokoroDiscoveryRetry
// so a server that starts later is still picked up.
func (c *KokoroClient) ensureVoicesLocked() {
	if len(c.discovered) > 0 || c.voicesURL == "" {
		return
	}
	if c.attempted && time.Since(c.lastAttempt) < kokoroDiscoveryRetry {
		return
	}
	c.attempted = true
	c.lastAttempt = time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), kokoroDiscoveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", c.voicesURL, nil)
	if err != nil {
		return
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	// Kokoro-FastAPI has shipped two shapes for this list over its releases:
	// plain strings, and objects carrying an id and a name. Decoding each entry
	// separately lets one parser handle both, and handle a mix of the two.
	var payload struct {
		Voices []json.RawMessage `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return
	}

	seen := make(map[string]bool, len(payload.Voices))
	for _, raw := range payload.Voices {
		name := kokoroVoiceName(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		c.discovered = append(c.discovered, name)
	}
}

// kokoroVoiceName pulls the usable voice name out of one entry of the server's
// voice list, whichever of the two shapes that entry uses. The id field wins
// over name because the id is what the speech endpoint accepts.
func kokoroVoiceName(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	if id := strings.TrimSpace(obj.ID); id != "" {
		return id
	}
	return strings.TrimSpace(obj.Name)
}

// DefaultVoice returns the voice used when none is specified. That is af_bella
// when the server has it, and otherwise the first voice the server reports, so
// the default is always a voice that actually exists on this server.
func (c *KokoroClient) DefaultVoice() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureVoicesLocked()

	if len(c.discovered) == 0 {
		return defaultKokoroVoice
	}
	for _, v := range c.discovered {
		if v == defaultKokoroVoice {
			return defaultKokoroVoice
		}
	}
	return c.discovered[0]
}

// Voices returns the voices the running Kokoro server reports. When the server
// cannot be reached it falls back to the short built-in list. Neither list is
// exhaustive of what Synthesize accepts, because Kokoro also takes weighted
// blends such as "af_bella(2)+af_sky(1)".
func (c *KokoroClient) Voices() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureVoicesLocked()

	src := c.discovered
	if len(src) == 0 {
		src = kokoroKnownVoices
	}
	voices := make([]string, len(src))
	copy(voices, src)
	return voices
}

// IsValidVoice accepts any non-empty voice string. Kokoro's voice set grows over
// time and supports weighted blends, so validation is left to the server, which
// returns a clear error for an unknown voice.
func (c *KokoroClient) IsValidVoice(voice string) bool {
	return strings.TrimSpace(voice) != ""
}

// kokoroRequest is the OpenAI-compatible request body Kokoro-FastAPI expects.
// response_format is set to mp3 so the rest of the pipeline, which assumes MP3
// bytes, is unchanged. Playback speed is applied later by the audio player (from
// TTS_SPEED), the same as for every other provider, so speed is not sent here.
type kokoroRequest struct {
	Model          string `json:"model"`
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

// SynthesizeStream starts synthesis and returns the response body as a stream
// of MP3 bytes. The caller must close the returned reader. Playback can begin
// as soon as the first bytes arrive, so the first sound is heard almost
// immediately rather than after the whole clip is synthesized.
func (c *KokoroClient) SynthesizeStream(text string, voice string) (io.ReadCloser, error) {
	reqBody := kokoroRequest{
		Model:          c.model,
		Input:          text,
		Voice:          voice,
		ResponseFormat: "mp3",
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	// No Authorization header: Kokoro runs locally and needs no API key.
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// The usual cause is that no Kokoro server is running at baseURL.
		return nil, fmt.Errorf("could not reach Kokoro server at %s (is it running?): %w", c.baseURL, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return resp.Body, nil
}

// Synthesize converts text to speech and returns the complete MP3 audio data.
// It is the buffered path: it reads the whole stream before returning.
func (c *KokoroClient) Synthesize(text string, voice string) ([]byte, error) {
	stream, err := c.SynthesizeStream(text, voice)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	audioData, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return audioData, nil
}
