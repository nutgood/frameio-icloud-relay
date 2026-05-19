// Package paths centralises the on-disk locations the relay reads and
// writes. Everything is anchored under the invoking user's home directory
// so the service is fully per-user (no /etc, no system-wide state).
package paths

import (
	"os"
	"path/filepath"
)

// Label is the LaunchAgent label and reverse-DNS bundle identifier used
// for files derived from it (plist filename, log filenames). Picked once
// to avoid collisions with any other agents on the host.
const Label = "sh.leca.frameio-icloud"

// Paths is a resolved set of locations for a given home directory.
type Paths struct {
	Home string

	// Support holds long-lived state: config, tokens, service state.
	Support string
	Config  string
	Tokens  string
	State   string

	// Downloads is the temporary buffer for files in flight between
	// Frame.io and Photos.app.
	Downloads string

	// LogDir / LogOut / LogErr are the launchd-redirected log paths.
	LogDir string
	LogOut string
	LogErr string

	// Plist is the LaunchAgent plist install location.
	LaunchAgentDir string
	Plist          string

	// Socket is the unix-domain socket the running service listens on
	// for status queries.
	Socket string

	// InstalledBinary is where `install` copies the binary so the plist
	// has a stable path to invoke.
	InstalledBinary string
}

// Default resolves Paths for the current user (os.UserHomeDir).
func Default() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return For(home), nil
}

// For resolves Paths for an explicit home directory. Tests use this with
// a t.TempDir() to avoid touching the real home.
func For(home string) *Paths {
	support := filepath.Join(home, "Library", "Application Support", "frameio-icloud")
	logs := filepath.Join(home, "Library", "Logs", "frameio-icloud")
	agents := filepath.Join(home, "Library", "LaunchAgents")
	return &Paths{
		Home:            home,
		Support:         support,
		Config:          filepath.Join(support, "config.json"),
		Tokens:          filepath.Join(support, "tokens.json"),
		State:           filepath.Join(support, "state.json"),
		Downloads:       filepath.Join(support, "downloads"),
		LogDir:          logs,
		LogOut:          filepath.Join(logs, "frameio-icloud.log"),
		LogErr:          filepath.Join(logs, "frameio-icloud.err"),
		LaunchAgentDir:  agents,
		Plist:           filepath.Join(agents, Label+".plist"),
		Socket:          filepath.Join(support, "status.sock"),
		InstalledBinary: filepath.Join(home, ".local", "bin", "frameio-icloud"),
	}
}

// EnsureDirs creates every directory the running service writes into.
// Idempotent and safe to call from `serve` startup.
func (p *Paths) EnsureDirs() error {
	for _, d := range []string{p.Support, p.Downloads, p.LogDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
