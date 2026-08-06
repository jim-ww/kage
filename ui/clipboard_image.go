package ui

import (
	"bytes"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// readClipboardImage fetches raw image bytes directly from the system
// clipboard, bypassing the terminal entirely. Bracketed-paste (Ctrl+V) can't
// carry binary clipboard content reliably: terminals run pasted bytes
// through the same escape-sequence parser used for keystrokes, so an image
// pasted that way gets mangled into garbage text and can stall the UI for
// megabytes of data (see the PasteImage keybind in keybinds.go, wired in
// update_keys.go). Going straight to wl-paste/xclip avoids the pty path
// altogether.
func readClipboardImage() ([]byte, error) {
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		return readClipboardImageWayland()
	case os.Getenv("DISPLAY") != "":
		return readClipboardImageX11()
	default:
		return nil, fmt.Errorf("no Wayland or X11 display detected")
	}
}

func readClipboardImageWayland() ([]byte, error) {
	if _, err := exec.LookPath("wl-paste"); err != nil {
		return nil, fmt.Errorf("wl-paste not found (install wl-clipboard)")
	}
	types, err := exec.Command("wl-paste", "--list-types").Output()
	if err != nil {
		return nil, fmt.Errorf("listing clipboard types: %w", err)
	}
	mimeType, ok := firstImageMIMEType(string(types))
	if !ok {
		return nil, fmt.Errorf("no image on clipboard")
	}
	out, err := exec.Command("wl-paste", "-t", mimeType, "-n").Output()
	if err != nil {
		return nil, fmt.Errorf("reading clipboard image: %w", err)
	}
	return out, nil
}

func readClipboardImageX11() ([]byte, error) {
	if _, err := exec.LookPath("xclip"); err != nil {
		return nil, fmt.Errorf("xclip not found")
	}
	targets, err := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return nil, fmt.Errorf("listing clipboard targets: %w", err)
	}
	mimeType, ok := firstImageMIMEType(string(targets))
	if !ok {
		return nil, fmt.Errorf("no image on clipboard")
	}
	out, err := exec.Command("xclip", "-selection", "clipboard", "-t", mimeType, "-o").Output()
	if err != nil {
		return nil, fmt.Errorf("reading clipboard image: %w", err)
	}
	return out, nil
}

// firstImageMIMEType picks the first "image/*" line out of a newline-listed
// set of MIME types (wl-paste --list-types / xclip -t TARGETS output),
// preferring png since it's lossless and universally supported.
func firstImageMIMEType(list string) (string, bool) {
	var first string
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "image/") {
			continue
		}
		if line == "image/png" {
			return line, true
		}
		if first == "" {
			first = line
		}
	}
	return first, first != ""
}

// writeClipboardImage saves clipboard image bytes to a temp file, sniffing
// the extension from content so it opens in the right viewer.
func writeClipboardImage(data []byte) (string, error) {
	mimeType := http.DetectContentType(data)
	ext := ".bin"
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}
	path := filepath.Join(os.TempDir(), "kage-clipboard-"+time.Now().Format("20060102-150405.000")+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// pasteClipboardImage is the PasteImage keybind's Cmd: fetch the clipboard
// image directly and report a path (or error) via clipboardImageResultMsg.
func pasteClipboardImage() tea.Msg {
	data, err := readClipboardImage()
	if err != nil {
		return clipboardImageResultMsg{err: err}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return clipboardImageResultMsg{err: fmt.Errorf("no image on clipboard")}
	}
	path, err := writeClipboardImage(data)
	if err != nil {
		return clipboardImageResultMsg{err: err}
	}
	return clipboardImageResultMsg{path: path}
}

type clipboardImageResultMsg struct {
	path string
	err  error
}
