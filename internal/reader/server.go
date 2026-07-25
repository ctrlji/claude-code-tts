package reader

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

//go:embed assets
var assetsFS embed.FS

// healthApp identifies this server in /api/health responses, so a second
// launch can tell "our reader already owns this port" apart from "some other
// program owns this port".
const healthApp = "claude-code-tts-reader"

// DefaultPort is where the reader listens unless TTS_READER_PORT or the -port
// flag says otherwise. If the port is taken by a foreign program, the reader
// falls back to a random free port.
const DefaultPort = 8898

// maxTTSChars bounds a single synthesis request. The page sends one sentence
// at a time, so real requests stay far below this; the bound matches the
// speak tool's limit.
const maxTTSChars = 4096

// Options configures a reader server.
type Options struct {
	Transcript      string                     // path to the session transcript (JSONL)
	Port            int                        // 0 means "any free port"
	Providers       map[string]tts.Synthesizer // nil means all built-in providers
	DefaultProvider string                     // "" means tts.DefaultProviderName()
	Speed           float64                    // initial playback speed; 0 means TTS_SPEED or 1.0
}

// Server is the local HTTP server behind the read-along page. It binds to the
// loopback interface only: the page shows conversation content and spends TTS
// API credits, so it must never be reachable from the network.
type Server struct {
	providers       map[string]tts.Synthesizer
	defaultProvider string
	speed           float64
	port            int

	mu         sync.Mutex // guards transcript and the watcher's last-seen state
	transcript string
	lastSize   int64
	lastMod    time.Time

	hub      *sseHub
	httpSrv  *http.Server
	listener net.Listener
	stop     chan struct{}
	stopOnce sync.Once
}

// New creates a reader server. Zero-value options fall back to the same
// defaults the speak tool uses, so the read-along voice matches what the user
// already hears.
func New(opts Options) *Server {
	providers := opts.Providers
	if providers == nil {
		providers = tts.NewProviders()
	}
	defaultProvider := opts.DefaultProvider
	if defaultProvider == "" {
		defaultProvider = tts.DefaultProviderName()
	}
	speed := opts.Speed
	if speed == 0 {
		speed = speedFromEnv()
	}
	return &Server{
		providers:       providers,
		defaultProvider: defaultProvider,
		speed:           speed,
		port:            opts.Port,
		transcript:      opts.Transcript,
		hub:             newSSEHub(),
		stop:            make(chan struct{}),
	}
}

// speedFromEnv reads TTS_SPEED (the same knob the audio player honors) and
// clamps it to the 0.5–2.0 range the page offers.
func speedFromEnv() float64 {
	v := strings.TrimSpace(os.Getenv("TTS_SPEED"))
	if v == "" {
		return 1.0
	}
	speed, err := strconv.ParseFloat(v, 64)
	if err != nil || speed < 0.5 || speed > 2.0 {
		return 1.0
	}
	return speed
}

// Start binds the loopback listener and begins serving in the background.
// It returns the page URL.
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil && s.port != 0 {
		// The launcher probes for an existing reader before starting a new
		// one, so a busy port here belongs to some other program.
		logging.Warn("reader: port %d is busy, using a random free port instead: %v", s.port, err)
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return "", fmt.Errorf("cannot listen on loopback: %w", err)
	}
	s.listener = ln
	s.primeWatcher()
	s.httpSrv = &http.Server{Handler: s.Handler()}
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logging.Error("reader: http server stopped: %v", err)
		}
	}()
	go s.watch()
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	logging.Info("reader: read-along page at %s (transcript: %s)", pageURL, s.transcript)
	return pageURL, nil
}

// Shutdown stops the HTTP server and the transcript watcher.
func (s *Server) Shutdown() {
	s.stopOnce.Do(func() { close(s.stop) })
	if s.httpSrv != nil {
		s.httpSrv.Close()
	}
}

// Handler returns the complete HTTP handler, wrapped in the loopback-only
// checks. Exported so tests can drive the server through httptest.
func (s *Server) Handler() http.Handler {
	assets, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err) // the embedded tree is fixed at compile time
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/messages", s.handleMessages)
	mux.HandleFunc("/api/tts", s.handleTTS)
	mux.HandleFunc("/api/load", s.handleLoad)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/events", s.handleEvents)
	return s.guard(mux)
}

// guard rejects requests that did not come from this machine's own browser.
// Binding to loopback alone does not stop a malicious web page from making
// the browser send requests to 127.0.0.1 (DNS rebinding, or plain cross-site
// POSTs that would burn TTS credits). Requiring a loopback Host header
// defeats rebinding; requiring a loopback Origin, whenever the browser sends
// one, defeats cross-site calls.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			u, err := url.Parse(origin)
			if err != nil || !isLoopbackHost(u.Host) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost reports whether a Host or Origin host (optionally with a
// port) names this machine's loopback interface.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "page missing from binary", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Everything the page needs is served from this origin; audio clips are
	// blob: URLs created from fetched MP3 bytes.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; media-src 'self' blob:; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
	if _, err := w.Write(page); err != nil {
		logging.Debug("reader: writing index: %v", err)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	transcript := s.transcript
	s.mu.Unlock()
	writeJSON(w, map[string]any{"app": healthApp, "transcript": transcript})
}

// providerConfig is what the page needs to offer a provider in its pickers.
type providerConfig struct {
	Voices       []string `json:"voices"`
	DefaultVoice string   `json:"default_voice"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	provs := make(map[string]providerConfig, len(s.providers))
	for name, synth := range s.providers {
		provs[name] = providerConfig{Voices: synth.Voices(), DefaultVoice: synth.DefaultVoice()}
	}
	// An on-the-fly voice override (tts-ctl voice NAME) applies to the default
	// provider, mirroring how the speak path treats TTS_VOICE.
	if v := os.Getenv("TTS_VOICE"); v != "" {
		if synth, ok := s.providers[s.defaultProvider]; ok && synth.IsValidVoice(v) {
			pc := provs[s.defaultProvider]
			pc.DefaultVoice = v
			provs[s.defaultProvider] = pc
		}
	}
	s.mu.Lock()
	transcript := s.transcript
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"default_provider": s.defaultProvider,
		"providers":        provs,
		"speed":            s.speed,
		"transcript":       transcript,
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	path := s.transcript
	s.mu.Unlock()
	msgs, err := ParseTranscript(path)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var version int64
	if fi, err := os.Stat(path); err == nil {
		version = fi.ModTime().UnixNano()
	}
	writeJSON(w, map[string]any{"transcript": path, "version": version, "messages": msgs})
}

func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Text     string `json:"text"`
		Provider string `json:"provider"`
		Voice    string `json:"voice"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSONError(w, http.StatusBadRequest, "text is required")
		return
	}
	if len(text) > maxTTSChars {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("text exceeds %d characters", maxTTSChars))
		return
	}
	provider := req.Provider
	if provider == "" {
		provider = s.defaultProvider
	}
	synth, ok := s.providers[provider]
	if !ok {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider %q", provider))
		return
	}
	voice := req.Voice
	if voice == "" {
		voice = synth.DefaultVoice()
	}
	if !synth.IsValidVoice(voice) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid voice %q for provider %s", voice, provider))
		return
	}
	audio, err := synth.Synthesize(text, voice)
	if err != nil {
		logging.Error("reader: synthesis failed (provider=%s, voice=%s): %v", provider, voice, err)
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(audio); err != nil {
		logging.Debug("reader: writing audio: %v", err)
	}
}

func (s *Server) handleLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		writeJSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("not a readable file: %s", path))
		return
	}
	s.mu.Lock()
	s.transcript = path
	// Force the watcher to treat the new file as changed on its next tick.
	s.lastSize, s.lastMod = -1, time.Time{}
	s.mu.Unlock()
	logging.Info("reader: switched transcript to %s", path)
	s.hub.broadcast(sseEvent{Name: "load", Data: path})
	writeJSON(w, map[string]any{"ok": true})
}

// handleStop tells every connected page to stop its playback. The page's
// audio lives in the browser, out of reach of any process kill, so this SSE
// relay is the only way `tts-ctl stop` can silence a read-along tab.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	s.hub.broadcast(sseEvent{Name: "stop", Data: "now"})
	writeJSON(w, map[string]any{"ok": true})
}

// handleEvents is the server-sent-events stream. The page listens here and
// refetches messages whenever the transcript changes, which is what makes the
// view follow the live session.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	fmt.Fprint(w, "event: hello\ndata: ok\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.stop:
			return
		case ev := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, ev.Data)
			fl.Flush()
		case <-ping.C:
			// Comment line: keeps proxies and the browser from timing out.
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// primeWatcher records the transcript's current size and mtime so the watcher
// only reports changes made after the server started.
func (s *Server) primeWatcher() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi, err := os.Stat(s.transcript); err == nil {
		s.lastSize, s.lastMod = fi.Size(), fi.ModTime()
	}
}

// watch polls the transcript once a second and notifies connected pages when
// it changed. Polling is deliberate: it needs no extra dependency, and a
// one-second delay is invisible next to speech-synthesis time.
func (s *Server) watch() {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		s.mu.Lock()
		path, size, mod := s.transcript, s.lastSize, s.lastMod
		s.mu.Unlock()
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if fi.Size() == size && fi.ModTime().Equal(mod) {
			continue
		}
		s.mu.Lock()
		s.lastSize, s.lastMod = fi.Size(), fi.ModTime()
		s.mu.Unlock()
		s.hub.broadcast(sseEvent{Name: "change", Data: strconv.FormatInt(fi.ModTime().UnixNano(), 10)})
	}
}

// sseEvent is one named server-sent event.
type sseEvent struct {
	Name string
	Data string
}

// sseHub fans events out to every connected page.
type sseHub struct {
	mu   sync.Mutex
	subs map[chan sseEvent]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{subs: make(map[chan sseEvent]struct{})}
}

func (h *sseHub) subscribe() chan sseEvent {
	ch := make(chan sseEvent, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *sseHub) unsubscribe(ch chan sseEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// broadcast never blocks: a page that has fallen behind misses an event and
// simply catches up on the next one it does receive.
func (h *sseHub) broadcast(ev sseEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.Debug("reader: writing JSON: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		logging.Debug("reader: writing JSON error: %v", err)
	}
}

// ProbeInstance reports whether a reader from this plugin is already
// listening on the given port.
func ProbeInstance(port int) bool {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		App string `json:"app"`
	}
	if json.NewDecoder(resp.Body).Decode(&h) != nil {
		return false
	}
	return h.App == healthApp
}

// SwitchTranscript points an already-running reader at another transcript,
// so a second `tts-ctl read` reuses the open page instead of starting a
// second server.
func SwitchTranscript(port int, path string) error {
	body, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/load", port), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("reader refused the transcript switch: %s", strings.TrimSpace(string(b)))
	}
	return nil
}
