package audio

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// playbackSpeed reads the TTS_SPEED environment variable and returns the
// playback rate to use. A rate of 1.0 means "play at normal speed". The second
// return value is false when no adjustment should be made — that is, when the
// variable is empty, unparseable, not positive, or exactly 1.0. The rate is
// clamped to the range the pitch-preserving filters accept: 0.5x to 2.0x.
func playbackSpeed() (rate float64, adjusted bool) {
	raw := strings.TrimSpace(os.Getenv("TTS_SPEED"))
	if raw == "" {
		return 1.0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 1.0, false
	}
	if v < 0.5 {
		v = 0.5
	}
	if v > 2.0 {
		v = 2.0
	}
	if v == 1.0 {
		return 1.0, false
	}
	return v, true
}

// Player handles audio playback with mutex protection
type Player struct {
	mu        sync.Mutex
	isPlaying bool
}

// NewPlayer creates a new audio player
func NewPlayer() *Player {
	return &Player{}
}

// Play plays the given audio data
// Only one audio can play at a time (mutex protected)
func (p *Player) Play(audioData []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.isPlaying = true
	defer func() { p.isPlaying = false }()

	// Create temporary file
	tmpFile, err := os.CreateTemp("", "tts-*.mp3")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(audioData); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write audio data: %w", err)
	}
	tmpFile.Close()

	// Resolve the optional playback-speed adjustment once, up front.
	speed, speedAdjusted := playbackSpeed()
	speedStr := strconv.FormatFloat(speed, 'f', -1, 64)

	// Play audio based on platform
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// afplay's -r sets the playback rate; only pass it when a speed was asked
		// for, so default behavior is unchanged.
		if speedAdjusted {
			cmd = exec.Command("afplay", "-r", speedStr, tmpFile.Name())
		} else {
			cmd = exec.Command("afplay", tmpFile.Name())
		}
	case "linux":
		// Try common Linux audio players
		if _, err := exec.LookPath("mpv"); err == nil {
			args := []string{"--no-video"}
			if speedAdjusted {
				// mpv keeps pitch steady when changing speed.
				args = append(args, "--speed="+speedStr)
			}
			args = append(args, tmpFile.Name())
			cmd = exec.Command("mpv", args...)
		} else if _, err := exec.LookPath("ffplay"); err == nil {
			args := []string{"-nodisp", "-autoexit"}
			if speedAdjusted {
				// atempo changes tempo without changing pitch (valid 0.5x–2.0x).
				args = append(args, "-af", "atempo="+speedStr)
			}
			args = append(args, tmpFile.Name())
			cmd = exec.Command("ffplay", args...)
		} else if _, err := exec.LookPath("aplay"); err == nil {
			// aplay requires WAV, so use mpg123 for MP3. mpg123 has no clean
			// pitch-preserving tempo control, so speed is not applied here.
			if _, err := exec.LookPath("mpg123"); err == nil {
				cmd = exec.Command("mpg123", "-q", tmpFile.Name())
			} else {
				return fmt.Errorf("no suitable audio player found on Linux (install mpv, ffplay, or mpg123)")
			}
		} else {
			return fmt.Errorf("no suitable audio player found on Linux (install mpv, ffplay, or mpg123)")
		}
	case "windows":
		// Windows Media Player via PowerShell
		cmd = exec.Command("powershell", "-c",
			fmt.Sprintf(`(New-Object Media.SoundPlayer '%s').PlaySync()`, tmpFile.Name()))
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("audio playback failed: %w", err)
	}

	return nil
}

// IsPlaying returns whether audio is currently playing
func (p *Player) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isPlaying
}
