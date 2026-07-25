// Companion extension for the claude-code-tts plugin.
//
// It exists because two things are impossible from outside the editor:
//
// 1. VS Code offers no command-line way to run a workbench command in an
//    already-running window, so a shell cannot open the Simple Browser by
//    itself. This extension watches a flag file that `tts-ctl read` writes
//    and opens the read-along view in response — a file-based bridge in the
//    style of pokey/command-server, protected by ordinary filesystem
//    permissions instead of an open localhost socket.
//
// 2. The Claude Code chat panel is a closed webview. No external process can
//    add UI to it — but a VS Code extension can contribute right-click menu
//    items to another extension's webview via the `webview/context` menu
//    point, scoped by that webview's view type. That is how "Read selection
//    aloud" appears in the actual chat panel. The selected text itself is
//    not exposed by the menu API, so the command reads the X11 primary
//    selection, which the highlight already populated.

const vscode = require('vscode');
const cp = require('child_process');
const fs = require('fs');
const path = require('path');
const os = require('os');

const PLUGIN_ROOT = path.join(os.homedir(), '.claude', 'plugins', 'claude-code-tts');
const TTS_CTL = path.join(PLUGIN_ROOT, 'bin', 'tts-ctl');
const OPEN_FLAG = path.join(PLUGIN_ROOT, 'reader.open');
const URL_FILE = path.join(PLUGIN_ROOT, 'reader.url');
const POLL_MS = 700;

function runTtsCtl(args) {
  cp.execFile(TTS_CTL, args, { env: process.env }, (err, stdout, stderr) => {
    if (err) {
      vscode.window.showWarningMessage(
        'claude-code-tts: ' + (String(stderr || '').trim() || err.message)
      );
    }
  });
}

function readerUrl() {
  try {
    const url = fs.readFileSync(URL_FILE, 'utf8').trim();
    if (url) return url;
  } catch (e) {
    // No reader has run yet; fall through to the default address.
  }
  return 'http://127.0.0.1:8898/';
}

function openReader(url) {
  vscode.commands.executeCommand('simpleBrowser.show', url || readerUrl());
}

function activate(context) {
  context.subscriptions.push(
    vscode.commands.registerCommand('claudeTts.readSelection', () => {
      const editor = vscode.window.activeTextEditor;
      if (editor && !editor.selection.isEmpty) {
        // Real editors expose their selection through the API — use it
        // directly so this also works on Wayland and macOS.
        runTtsCtl(['say', editor.document.getText(editor.selection)]);
        return;
      }
      // Webviews (the Claude Code chat panel) do not expose selections, but
      // highlighting already placed the text in the X11 primary selection.
      runTtsCtl(['selection']);
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('claudeTts.stop', () => runTtsCtl(['stop']))
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('claudeTts.openReader', () => openReader())
  );

  // Zero-click bridge: `tts-ctl read` writes the page URL into reader.open;
  // seeing it appear, we open the read-along view as an editor tab and
  // remove the flag. A pre-existing (stale) flag is ignored on activation so
  // reloading the window never pops the view uninvited.
  let lastMtime = Infinity;
  try {
    lastMtime = fs.statSync(OPEN_FLAG).mtimeMs;
  } catch (e) {
    lastMtime = 0;
  }
  const timer = setInterval(() => {
    fs.stat(OPEN_FLAG, (err, st) => {
      if (err || st.mtimeMs <= lastMtime) return;
      lastMtime = st.mtimeMs;
      fs.readFile(OPEN_FLAG, 'utf8', (readErr, data) => {
        fs.unlink(OPEN_FLAG, () => {});
        openReader(readErr ? undefined : String(data).trim() || undefined);
      });
    });
  }, POLL_MS);
  context.subscriptions.push({ dispose: () => clearInterval(timer) });
}

function deactivate() {}

module.exports = { activate, deactivate };
