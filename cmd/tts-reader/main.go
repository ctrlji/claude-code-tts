// Command tts-reader opens the read-along view for a Claude Code session:
// the conversation rendered in a local web page that reads it aloud while
// highlighting the sentence and word being spoken, with read-on-select.
//
// It is normally started through `tts-ctl read` (the /tts-read slash
// command), which loads the plugin configuration first so provider API keys
// and defaults are present in the environment.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/reader"
)

func main() {
	transcript := flag.String("transcript", "", "path to a session transcript (.jsonl) or a Markdown/text file to read; default: the newest session of the project directory")
	projectDir := flag.String("project-dir", "", "project directory used to locate the session (default: $CLAUDE_PROJECT_DIR, else the working directory)")
	port := flag.Int("port", portFromEnv(), "port to listen on (127.0.0.1 only); a busy port falls back to a random free one")
	noOpen := flag.Bool("no-open", false, "do not open the page anywhere")
	browser := flag.Bool("browser", false, "open the system web browser even when running inside VS Code")
	urlFile := flag.String("url-file", "", "write the page URL to this file once the server is ready")
	files := flag.Bool("files", false, "open the navigator at this project's Markdown and text files instead of a conversation")
	flag.Parse()

	if err := logging.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: file logging unavailable: %v\n", err)
	}

	// The project directory is needed for both the default transcript lookup
	// and the -files listing.
	dir := *projectDir
	if dir == "" {
		dir = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatal("cannot determine the working directory: %v", err)
		}
		dir = wd
	}

	// -files opens the navigator with this project's document tree expanded,
	// so no session has to be loaded at all.
	if *files {
		openFilesView(dir, *port, *urlFile, *noOpen, *browser)
		return
	}

	path := *transcript
	if path == "" {
		found, err := reader.FindLatestTranscript(dir)
		if err != nil {
			fatal("%v\nPass the transcript directly: tts-reader -transcript /path/to/session.jsonl", err)
		}
		path = found
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		fatal("not a readable file: %s", path)
	}

	// If a reader from this plugin is already running, register the session
	// with it and reuse the running server instead of starting a second one.
	// Each session has its own page URL, so other open tabs are undisturbed.
	if reuseRunning(*port) {
		pageURL, err := reader.SwitchTranscript(*port, path)
		if err != nil {
			fatal("a reader already runs on port %d but did not accept the transcript: %v", *port, err)
		}
		finish(pageURL, *urlFile, *noOpen, *browser)
		return
	}

	srv := reader.New(reader.Options{Transcript: path, Port: *port})
	pageURL, err := srv.Start()
	if err != nil {
		fatal("%v", err)
	}
	finish(pageURL, *urlFile, *noOpen, *browser)
	waitForExit(srv)
}

// reuseRunning decides whether to hand this launch over to a reader that is
// already listening on the port. It reuses one built from the same binary,
// and replaces one built from a different binary.
//
// Replacing matters because the page and the whole HTTP API are compiled into
// the binary. A reader started before a rebuild keeps serving the old page,
// so new work appears to have had no effect — the symptom looks like a broken
// feature rather than a stale process. Returning false means "the port is
// yours now": the old reader has already quit.
func reuseRunning(port int) bool {
	inst := reader.Probe(port)
	if !inst.Running {
		return false
	}
	if !inst.Stale() {
		return true
	}
	if reader.RetireInstance(port) {
		fmt.Fprintln(os.Stderr, "replaced the reader that was running on port", port, "— it came from an older build")
		return false
	}
	// It would not stand down (an older build has no quit endpoint). Reusing
	// it still shows the conversation, so say what is happening rather than
	// failing the launch.
	fmt.Fprintf(os.Stderr, "note: the reader on port %d is from an older build and would not restart; "+
		"its page may be out of date. Stop it and run this again to pick up the new build.\n", port)
	return true
}

// waitForExit blocks until the process is asked to stop, either by a signal or
// by a newer build taking over the port through /api/quit.
func waitForExit(srv *reader.Server) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
	case <-srv.Done():
	}
	srv.Shutdown()
}

// openFilesView opens the navigator page with one project's document tree
// already expanded — the `tts-ctl read --files` entry point. The navigator
// needs no session, so this reuses a running reader when there is one and
// otherwise starts a server with nothing loaded. The page itself asks the
// server for the file listing through /api/docs.
func openFilesView(dir string, port int, urlFile string, noOpen, forceBrowser bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		fatal("cannot resolve the project directory %s: %v", dir, err)
	}
	query := "/?docs=" + url.QueryEscape(abs)

	if reuseRunning(port) {
		finish(fmt.Sprintf("http://127.0.0.1:%d%s", port, query), urlFile, noOpen, forceBrowser)
		return
	}

	srv := reader.New(reader.Options{Port: port})
	pageURL, err := srv.Start()
	if err != nil {
		fatal("%v", err)
	}
	// Start() reports the bare navigator URL (there is no session); the docs
	// parameter is what makes it land on this project.
	finish(strings.TrimSuffix(pageURL, "/")+query, urlFile, noOpen, forceBrowser)
	waitForExit(srv)
}

// portFromEnv honors TTS_READER_PORT so the port can be set once in the
// plugin config instead of on every launch.
func portFromEnv() int {
	if v := os.Getenv("TTS_READER_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return reader.DefaultPort
}

// finish reports the page URL: on stdout always, into the URL file when
// asked (the tts-read launcher polls that file), and then opens it — unless
// the process runs inside VS Code, where the URL is left as a clickable link
// instead. A workbench.externalUriOpeners rule (see the README) makes that
// click open the page in VS Code's built-in Simple Browser tab, so the user
// stays in the editor; launching the system browser would pull them out of
// it, which is why that path needs an explicit -browser.
func finish(pageURL, urlFile string, noOpen, forceBrowser bool) {
	fmt.Println(pageURL)
	if urlFile != "" {
		if err := os.WriteFile(urlFile, []byte(pageURL+"\n"), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot write %s: %v\n", urlFile, err)
		}
	}
	if noOpen {
		return
	}
	if inVSCode() && !forceBrowser {
		fmt.Println("running inside VS Code: click the URL above to open the page as an editor tab (pass --browser for the system browser)")
		return
	}
	if err := openBrowser(pageURL); err != nil {
		fmt.Fprintf(os.Stderr, "open %s yourself (auto-open failed: %v)\n", pageURL, err)
	}
}

// inVSCode reports whether this process was started from within VS Code —
// either an integrated terminal or the Claude Code extension's shell.
func inVSCode() bool {
	return os.Getenv("TERM_PROGRAM") == "vscode" ||
		os.Getenv("VSCODE_PID") != "" ||
		os.Getenv("VSCODE_IPC_HOOK") != ""
}

func openBrowser(pageURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", pageURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", pageURL)
	default:
		cmd = exec.Command("xdg-open", pageURL)
	}
	return cmd.Start()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
