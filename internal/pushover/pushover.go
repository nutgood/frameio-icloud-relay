// Package pushover sends notifications via the Pushover Messages API.
// See https://pushover.net/api for the full schema; we use the minimum
// needed for this relay's notifications.
package pushover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the production Pushover Messages endpoint. Overridable
// for tests via Client.Endpoint.
const DefaultEndpoint = "https://api.pushover.net/1/messages.json"

// Client is a minimal Pushover HTTP client.
type Client struct {
	AppToken string // Pushover application API token
	UserKey  string // Recipient user key
	Endpoint string // overrides DefaultEndpoint when non-empty (tests)
	HTTP     *http.Client
}

// New constructs a Client. AppToken / UserKey come from
// https://pushover.net/api → "Your User Key" and the application's API token.
func New(appToken, userKey string) *Client {
	return &Client{
		AppToken: appToken,
		UserKey:  userKey,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Message is the payload for a single push. Title is optional; Priority
// 0 is normal, 1 is high (bypass quiet hours).
type Message struct {
	Title    string
	Body     string
	Priority int
}

// Enabled reports whether the client has credentials configured. When
// false, Send is a no-op — useful so callers can wire the client in
// unconditionally even when the user hasn't supplied Pushover keys.
func (c *Client) Enabled() bool {
	return c != nil && c.AppToken != "" && c.UserKey != ""
}

// Send posts a message. No-op if the client isn't fully configured.
func (c *Client) Send(ctx context.Context, m Message) error {
	if !c.Enabled() {
		return nil
	}
	if m.Body == "" {
		return errors.New("pushover: empty body")
	}
	form := url.Values{}
	form.Set("token", c.AppToken)
	form.Set("user", c.UserKey)
	form.Set("message", m.Body)
	if m.Title != "" {
		form.Set("title", m.Title)
	}
	if m.Priority != 0 {
		form.Set("priority", strconv.Itoa(m.Priority))
	}

	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpc := c.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("pushover: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("pushover: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
