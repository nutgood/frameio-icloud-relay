// Package config persists user-editable configuration for the relay
// (Frame.io scope, Pushover credentials, public URL, etc.). The on-disk
// form is JSON for zero-dependency parsing; the CLI exposes get/set
// subcommands so users rarely have to edit it by hand.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the full user configuration. Fields are flat (no nesting) so
// get/set by dotted key is trivial to implement.
type Config struct {
	// Frame.io scope. AccountID / WorkspaceID / FolderID are auto-discovered
	// on first `serve` if there's exactly one of each.
	FrameioAccount   string `json:"frameio_account,omitempty"`
	FrameioWorkspace string `json:"frameio_workspace,omitempty"`
	FrameioFolder    string `json:"frameio_folder,omitempty"`

	// Optional public HTTPS URL routing to the relay's webhook listener.
	// Empty => polling-only mode.
	PublicURL string `json:"public_url,omitempty"`

	// Pushover notification credentials. Both must be set to enable
	// notifications.
	PushoverToken   string `json:"pushover_token,omitempty"`
	PushoverUserKey string `json:"pushover_user_key,omitempty"`

	// Webhook server bind address. Defaults to ":9000" when empty.
	WebhookAddr string `json:"webhook_addr,omitempty"`

	// Reconcile poll interval (Go duration string). Defaults to "60s"
	// when empty.
	PollInterval string `json:"poll_interval,omitempty"`

	// Stuck-upload reaper threshold (Go duration string). Empty/"0" =
	// disabled.
	StuckTimeout string `json:"stuck_timeout,omitempty"`
}

// Load reads config from path. Returns a zero-value Config if the file
// doesn't exist (so a fresh install can `config set` into it).
func Load(path string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return c, nil
}

// Save atomically writes config to path with 0600 perms (it contains
// Pushover credentials).
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// keys maps dotted user-facing keys to struct field pointers for
// type-checked get/set. New keys go here and as a tagged field on Config.
func (c *Config) keys() map[string]*string {
	return map[string]*string{
		"frameio.account":   &c.FrameioAccount,
		"frameio.workspace": &c.FrameioWorkspace,
		"frameio.folder":    &c.FrameioFolder,
		"public_url":        &c.PublicURL,
		"pushover.token":    &c.PushoverToken,
		"pushover.user_key": &c.PushoverUserKey,
		"webhook_addr":      &c.WebhookAddr,
		"poll_interval":     &c.PollInterval,
		"stuck_timeout":     &c.StuckTimeout,
	}
}

// Get returns the string value of a dotted key, or an error if the key
// is unknown.
func (c *Config) Get(key string) (string, error) {
	ptr, ok := c.keys()[key]
	if !ok {
		return "", fmt.Errorf("unknown config key %q (valid: %s)", key, strings.Join(c.ValidKeys(), ", "))
	}
	return *ptr, nil
}

// Set assigns a dotted key's value, or returns an error if the key is
// unknown.
func (c *Config) Set(key, value string) error {
	ptr, ok := c.keys()[key]
	if !ok {
		return fmt.Errorf("unknown config key %q (valid: %s)", key, strings.Join(c.ValidKeys(), ", "))
	}
	*ptr = value
	return nil
}

// ValidKeys returns the supported dotted keys for error messages and
// shell completion. Order is stable.
func (c *Config) ValidKeys() []string {
	return []string{
		"frameio.account",
		"frameio.workspace",
		"frameio.folder",
		"public_url",
		"pushover.token",
		"pushover.user_key",
		"webhook_addr",
		"poll_interval",
		"stuck_timeout",
	}
}

// Redacted returns a copy with secrets replaced by "<set>" / "<unset>",
// safe for printing in `frameio-icloud config` output and status dumps.
func (c *Config) Redacted() Config {
	r := *c
	mask := func(v string) string {
		if v == "" {
			return ""
		}
		return "<set>"
	}
	r.PushoverToken = mask(r.PushoverToken)
	r.PushoverUserKey = mask(r.PushoverUserKey)
	return r
}
