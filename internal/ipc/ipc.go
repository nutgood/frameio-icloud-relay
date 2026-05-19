// Package ipc serves a tiny HTTP-over-unix-socket status endpoint from
// the running service, and provides the matching client used by the CLI's
// `status` subcommand. JSON in, JSON out; no auth (socket file perms
// gate access — 0600).
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Status is the snapshot the service publishes. Kept flat-ish so the
// CLI can pretty-print without nested decoding logic.
type Status struct {
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	AuthUser       string    `json:"auth_user,omitempty"`
	AuthExpiresAt  time.Time `json:"auth_expires_at,omitempty"`
	WebhookURL     string    `json:"webhook_url,omitempty"`
	WebhookActive  bool      `json:"webhook_active"`
	PollInterval   string    `json:"poll_interval,omitempty"`
	PollingOnly    bool      `json:"polling_only"`
	InFlight       []string  `json:"in_flight,omitempty"`
	RecentImports  []Event   `json:"recent_imports,omitempty"`
	RecentErrors   []Event   `json:"recent_errors,omitempty"`
	BurstOpen      bool      `json:"burst_open"`
	BurstCount     int       `json:"burst_count"`
	BurstFailed    int       `json:"burst_failed"`
	BurstStartedAt time.Time `json:"burst_started_at,omitempty"`
}

// Event is a single timestamped log line for the status page (an import
// success / an error, etc.). Used in recent_imports and recent_errors.
type Event struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

// Provider is the callback the service implements to publish a fresh
// snapshot each time the CLI asks.
type Provider func() Status

// Serve runs an HTTP server on the unix socket at path until ctx is
// cancelled. The socket file is removed on shutdown. If a stale socket
// from a previous run exists at path, it's unlinked first.
func Serve(ctx context.Context, path string, provider Provider) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("ipc listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("ipc chmod: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(provider())
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = os.Remove(path)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Fetch dials the unix socket at path and asks for /status. Returns a
// distinguishing error when the socket file is missing so the CLI can
// print a friendly "service not running" message.
func Fetch(ctx context.Context, path string) (*Status, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotRunning
	}
	httpc := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://unix/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, ErrNotRunning
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status: %s", resp.Status)
	}
	var s Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ErrNotRunning is returned by Fetch when the socket file is absent or
// not accepting connections — the typical "service is not running" case.
var ErrNotRunning = errors.New("service not running (no listener at status socket)")
