// Package photos imports files into the macOS Photos.app library via
// AppleScript. Once imported, iCloud Photos sync (if enabled in System
// Settings) uploads the asset to iCloud automatically — that's outside
// this process.
//
// Requires:
//   - macOS host with Photos.app installed
//   - The invoking user must have granted the binary Automation permission
//     for Photos (System Settings → Privacy & Security → Automation). The
//     first import will prompt; subsequent runs are silent.
//   - A logged-in GUI session — Photos.app cannot be driven from a daemon
//     context, which is why the relay runs as a LaunchAgent.
package photos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Importer drives Photos.app via osascript.
type Importer struct {
	// Binary is the osascript binary to invoke. Defaults to "osascript"
	// (found on $PATH) when zero.
	Binary string
}

// New returns an Importer with default settings.
func New() *Importer {
	return &Importer{}
}

// Import adds a single file at path into the Photos library. Returns once
// Photos.app has acknowledged the import; the resulting asset's iCloud
// upload is asynchronous and not awaited here.
//
// AppleScript's `import` is idempotent only in the loose sense — calling
// it twice with the same file path will result in two library entries.
// Callers must dedupe upstream (the relay does this by tracking which
// Frame.io file IDs have been imported and only invoking Import once per
// ID, then deleting the Frame.io source).
func (i *Importer) Import(ctx context.Context, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("photos: abs path: %w", err)
	}
	// The `with skip check duplicates` clause tells Photos.app not to
	// silently drop the file if it looks like a duplicate of an existing
	// library asset. We dedupe on Frame.io file ID upstream; we want
	// every file the relay handles to land in Photos so iCloud sync sees
	// it. AppleScript string escaping: only backslashes and quotes need
	// attention. macOS file paths can't contain NUL, so that's safe.
	escaped := strings.ReplaceAll(abs, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	script := fmt.Sprintf(`tell application "Photos"
	import {POSIX file "%s"} with skip check duplicates
end tell`, escaped)

	binary := i.Binary
	if binary == "" {
		binary = "osascript"
	}
	cmd := exec.CommandContext(ctx, binary, "-e", script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// Surface the common case: user hasn't granted Automation
		// permission for Photos. AppleScript reports this as either
		// "Not authorized to send Apple events" or error -1743.
		if strings.Contains(msg, "Not authorized") || strings.Contains(msg, "-1743") {
			return fmt.Errorf("photos: Automation permission denied — grant access in System Settings → Privacy & Security → Automation → (this binary) → Photos: %s", msg)
		}
		return fmt.Errorf("photos: osascript: %s", msg)
	}
	return nil
}

// Check verifies that osascript is on PATH and that Photos.app responds.
// Returns nil if a no-op AppleScript against Photos succeeds.
func (i *Importer) Check(ctx context.Context) error {
	binary := i.Binary
	if binary == "" {
		binary = "osascript"
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("photos: %s not found on PATH", binary)
	}
	cmd := exec.CommandContext(ctx, binary, "-e", `tell application "Photos" to get name`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New("photos: check: " + msg)
	}
	return nil
}
