package reader

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
)

// The reader keeps running after the command that started it returns, and a
// later launch reuses it instead of starting a second server. That reuse is
// what makes a rebuild look like it did nothing: the new binary hands the
// browser back to the old process, which still serves the old page assets and
// the old API. BuildID gives the launcher a way to notice, by fingerprinting
// the executable each server was started from.
var (
	buildOnce sync.Once
	buildID   string
)

// BuildID returns a short fingerprint of the running executable: the first 12
// hex characters of its SHA-256 hash. Two processes started from the same
// binary report the same value; rebuild or reinstall the binary and the value
// changes. It returns an empty string when the executable cannot be read,
// which callers must treat as "unknown" rather than "different".
func BuildID() string {
	buildOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			logging.Debug("reader: cannot locate the running executable: %v", err)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			logging.Debug("reader: cannot read %s for a build id: %v", path, err)
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			logging.Debug("reader: cannot hash %s: %v", path, err)
			return
		}
		buildID = hex.EncodeToString(h.Sum(nil))[:12]
	})
	return buildID
}
