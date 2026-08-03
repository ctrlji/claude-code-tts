package tts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewKokoroClient_DefaultBaseURL(t *testing.T) {
	t.Setenv("KOKORO_BASE_URL", "")

	client := NewKokoroClient()

	if client.Name() != ProviderKokoro {
		t.Errorf("expected name %q, got %q", ProviderKokoro, client.Name())
	}
	if client.model != "kokoro" {
		t.Errorf("expected model 'kokoro', got %q", client.model)
	}
	// Clearing voicesURL switches voice discovery off, so this test never makes
	// a network call and never depends on whether a Kokoro server happens to be
	// running on the machine running the tests.
	client.voicesURL = ""
	if client.DefaultVoice() != defaultKokoroVoice {
		t.Errorf("expected default voice %q, got %q", defaultKokoroVoice, client.DefaultVoice())
	}
	want := defaultKokoroBaseURL + kokoroSpeechPath
	if client.baseURL != want {
		t.Errorf("expected baseURL %q, got %q", want, client.baseURL)
	}
}

func TestNewKokoroClient_CustomBaseURL(t *testing.T) {
	t.Setenv("KOKORO_BASE_URL", "http://box:9000")

	client := NewKokoroClient()

	if client.baseURL != "http://box:9000"+kokoroSpeechPath {
		t.Errorf("unexpected baseURL %q", client.baseURL)
	}
	if client.voicesURL != "http://box:9000"+kokoroVoicesPath {
		t.Errorf("unexpected voicesURL %q", client.voicesURL)
	}
}

func TestKokoroVoicesEndpoint(t *testing.T) {
	tests := []struct {
		root string
		want string
	}{
		{"http://localhost:8880", "http://localhost:8880/v1/audio/voices"},
		{"http://localhost:8880/", "http://localhost:8880/v1/audio/voices"},
		{"  http://localhost:8880  ", "http://localhost:8880/v1/audio/voices"},
		// KOKORO_BASE_URL is allowed to point straight at the speech endpoint,
		// so that suffix has to be stripped before the voices path is added.
		{"http://localhost:8880/v1/audio/speech", "http://localhost:8880/v1/audio/voices"},
	}

	for _, tt := range tests {
		t.Run(tt.root, func(t *testing.T) {
			if got := kokoroVoicesEndpoint(tt.root); got != tt.want {
				t.Errorf("kokoroVoicesEndpoint(%q) = %q, want %q", tt.root, got, tt.want)
			}
		})
	}
}

// newTestKokoroVoicesServer serves a voice list in the shape the caller asks
// for and reports how many times it was asked.
func newTestKokoroVoicesServer(t *testing.T, body string, status int, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Path != kokoroVoicesPath {
			t.Errorf("expected path %s, got %s", kokoroVoicesPath, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestKokoroClient_Voices_DiscoversFromServer(t *testing.T) {
	// The two shapes Kokoro-FastAPI has shipped, mixed in one response.
	body := `{"voices":[{"id":"bf_lily","name":"Lily"},"am_adam",{"name":"af_kore"}]}`
	calls := 0
	server := newTestKokoroVoicesServer(t, body, http.StatusOK, &calls)
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		model:      "kokoro",
		baseURL:    server.URL + kokoroSpeechPath,
		voicesURL:  server.URL + kokoroVoicesPath,
	}

	got := client.Voices()
	want := []string{"bf_lily", "am_adam", "af_kore"}
	if len(got) != len(want) {
		t.Fatalf("Voices() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Voices()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// A second call must come from the cache, not from the server.
	client.Voices()
	if calls != 1 {
		t.Errorf("expected 1 discovery request, got %d", calls)
	}
}

func TestKokoroClient_Voices_FallsBackWhenServerUnreachable(t *testing.T) {
	calls := 0
	server := newTestKokoroVoicesServer(t, "boom", http.StatusInternalServerError, &calls)
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		model:      "kokoro",
		baseURL:    server.URL + kokoroSpeechPath,
		voicesURL:  server.URL + kokoroVoicesPath,
	}

	got := client.Voices()
	if len(got) != len(kokoroKnownVoices) || got[0] != kokoroKnownVoices[0] {
		t.Errorf("expected the built-in list, got %v", got)
	}
	// A failure must not be cached the way a success is: after the retry delay
	// the client asks again, which is how a server started later is picked up.
	client.lastAttempt = time.Now().Add(-2 * kokoroDiscoveryRetry)
	client.Voices()
	if calls != 2 {
		t.Errorf("expected a retry after the delay, got %d requests", calls)
	}
}

func TestKokoroClient_DefaultVoice_UsesServerListWhenBellaMissing(t *testing.T) {
	calls := 0
	server := newTestKokoroVoicesServer(t, `{"voices":["bf_lily","am_adam"]}`, http.StatusOK, &calls)
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		voicesURL:  server.URL + kokoroVoicesPath,
	}

	if got := client.DefaultVoice(); got != "bf_lily" {
		t.Errorf("DefaultVoice() = %q, want bf_lily", got)
	}
}

func TestKokoroClient_DefaultVoice_PrefersBellaWhenPresent(t *testing.T) {
	calls := 0
	server := newTestKokoroVoicesServer(t, `{"voices":["bf_lily","af_bella"]}`, http.StatusOK, &calls)
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		voicesURL:  server.URL + kokoroVoicesPath,
	}

	if got := client.DefaultVoice(); got != defaultKokoroVoice {
		t.Errorf("DefaultVoice() = %q, want %q", got, defaultKokoroVoice)
	}
}

func TestKokoroEndpoint(t *testing.T) {
	tests := []struct {
		root string
		want string
	}{
		{"http://localhost:8880", "http://localhost:8880/v1/audio/speech"},
		{"http://localhost:8880/", "http://localhost:8880/v1/audio/speech"},
		{"  http://localhost:8880  ", "http://localhost:8880/v1/audio/speech"},
		{"http://localhost:8880/v1/audio/speech", "http://localhost:8880/v1/audio/speech"},
	}

	for _, tt := range tests {
		t.Run(tt.root, func(t *testing.T) {
			if got := kokoroEndpoint(tt.root); got != tt.want {
				t.Errorf("kokoroEndpoint(%q) = %q, want %q", tt.root, got, tt.want)
			}
		})
	}
}

func TestKokoroClient_IsValidVoice(t *testing.T) {
	client := NewKokoroClient()

	tests := []struct {
		voice    string
		expected bool
	}{
		{"af_bella", true},
		{"am_michael", true},
		{"af_bella(2)+af_sky(1)", true}, // weighted blends pass through
		{"anything_new", true},          // unknown names are left to the server
		{"", false},
		{"   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.voice, func(t *testing.T) {
			if got := client.IsValidVoice(tt.voice); got != tt.expected {
				t.Errorf("IsValidVoice(%q) = %v, want %v", tt.voice, got, tt.expected)
			}
		})
	}
}

func TestKokoroClient_Synthesize_Success(t *testing.T) {
	expectedAudio := []byte("fake-mp3-audio-data")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		// Kokoro is keyless: no Authorization header should be sent.
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected no Authorization header, got %q", auth)
		}

		body, _ := io.ReadAll(r.Body)
		var req kokoroRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("failed to unmarshal request: %v", err)
		}
		if req.Model != "kokoro" {
			t.Errorf("expected model kokoro, got %s", req.Model)
		}
		if req.Input != "Hello, world!" {
			t.Errorf("expected input 'Hello, world!', got %s", req.Input)
		}
		if req.Voice != "af_bella" {
			t.Errorf("expected voice af_bella, got %s", req.Voice)
		}
		if req.ResponseFormat != "mp3" {
			t.Errorf("expected response_format mp3, got %s", req.ResponseFormat)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(expectedAudio)
	}))
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		model:      "kokoro",
		baseURL:    server.URL,
	}

	audio, err := client.Synthesize("Hello, world!", "af_bella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(audio) != string(expectedAudio) {
		t.Errorf("expected audio %q, got %q", expectedAudio, audio)
	}
}

func TestKokoroClient_Synthesize_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail": "unknown voice"}`))
	}))
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		model:      "kokoro",
		baseURL:    server.URL,
	}

	if _, err := client.Synthesize("Hello", "nope"); err == nil {
		t.Error("expected error for API failure")
	}
}

func TestKokoroClient_SynthesizeStream_Success(t *testing.T) {
	expectedAudio := []byte("streamed-mp3-bytes")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(expectedAudio)
	}))
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		model:      "kokoro",
		baseURL:    server.URL,
	}

	stream, err := client.SynthesizeStream("Hello, world!", "af_bella")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stream.Close()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	if string(got) != string(expectedAudio) {
		t.Errorf("expected audio %q, got %q", expectedAudio, got)
	}
}

func TestKokoroClient_SynthesizeStream_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail": "unknown voice"}`))
	}))
	defer server.Close()

	client := &KokoroClient{
		httpClient: server.Client(),
		model:      "kokoro",
		baseURL:    server.URL,
	}

	stream, err := client.SynthesizeStream("Hello", "nope")
	if err == nil {
		if stream != nil {
			stream.Close()
		}
		t.Error("expected error for API failure")
	}
}
