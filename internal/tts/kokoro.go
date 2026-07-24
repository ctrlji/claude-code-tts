package tts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultKokoroBaseURL is the address Kokoro-FastAPI listens on out of the box.
// KOKORO_BASE_URL overrides it. The value is the server root (host and port);
// the OpenAI-compatible speech path is appended to it.
const defaultKokoroBaseURL = "http://localhost:8880"

// kokoroSpeechPath is the OpenAI-compatible endpoint Kokoro-FastAPI exposes.
const kokoroSpeechPath = "/v1/audio/speech"

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
			// Local CPU synthesis can be slower than a cloud API, so allow
			// generous time even though long text is chunked before it arrives.
			Timeout: 120 * time.Second,
		},
		model:   "kokoro",
		baseURL: kokoroEndpoint(root),
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

// Name returns the provider identifier.
func (c *KokoroClient) Name() string {
	return ProviderKokoro
}

// DefaultVoice returns the voice used when none is specified.
func (c *KokoroClient) DefaultVoice() string {
	return defaultKokoroVoice
}

// Voices returns a short list of well-known Kokoro voices for help text. The
// server accepts many more, plus weighted blends, so this list is not exhaustive.
func (c *KokoroClient) Voices() []string {
	voices := make([]string, len(kokoroKnownVoices))
	copy(voices, kokoroKnownVoices)
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

// Synthesize converts text to speech and returns MP3 audio data.
func (c *KokoroClient) Synthesize(text string, voice string) ([]byte, error) {
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return audioData, nil
}
