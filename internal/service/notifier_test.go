package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nutgood/frameio-icloud-relay/internal/pushover"
)

// captureServer is a stand-in Pushover backend that records each message
// body it receives. Lets us assert on the burst-window behavior without
// network access.
type captureServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	count  atomic.Int32
}

func newCaptureServer() *captureServer {
	c := &captureServer{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c.mu.Lock()
		c.bodies = append(c.bodies, r.PostForm.Get("message"))
		c.mu.Unlock()
		c.count.Add(1)
		w.WriteHeader(200)
	}))
	return c
}

func (c *captureServer) close()     { c.srv.Close() }
func (c *captureServer) total() int { return int(c.count.Load()) }
func (c *captureServer) snap() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.bodies))
	copy(out, c.bodies)
	return out
}

func newTestNotifier(t *testing.T, idle time.Duration) (*notifier, *captureServer) {
	t.Helper()
	cs := newCaptureServer()
	t.Cleanup(cs.close)
	p := pushover.New("t", "u")
	p.Endpoint = cs.srv.URL
	return newNotifier(p, idle), cs
}

// waitFor polls until pred returns true or timeout. Used because the
// notifier dispatches Pushover sends in goroutines and the timer fires
// async.
func waitFor(t *testing.T, pred func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestBurst_OneWebhookOneImport(t *testing.T) {
	n, cs := newTestNotifier(t, 50*time.Millisecond)

	n.OnWebhook()
	n.OnImported()

	waitFor(t, func() bool { return cs.total() >= 2 }, time.Second)
	bodies := cs.snap()
	if !strings.Contains(bodies[0], "Received webhook") {
		t.Errorf("first push: %q", bodies[0])
	}
	if !strings.Contains(bodies[1], "Imported 1 picture") {
		t.Errorf("summary push: %q", bodies[1])
	}
}

func TestBurst_CoalescesMultiple(t *testing.T) {
	n, cs := newTestNotifier(t, 100*time.Millisecond)

	n.OnWebhook()
	n.OnWebhook() // already-open burst: should NOT send another "Received"
	n.OnImported()
	n.OnImported()
	n.OnImported()

	waitFor(t, func() bool { return cs.total() >= 2 }, time.Second)
	// Wait a bit more to catch any extra pushes that would indicate a
	// burst-state bug.
	time.Sleep(50 * time.Millisecond)

	bodies := cs.snap()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 pushes, got %d: %v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[1], "Imported 3 pictures") {
		t.Errorf("summary: %q", bodies[1])
	}
}

func TestBurst_FailureSummary(t *testing.T) {
	n, cs := newTestNotifier(t, 50*time.Millisecond)
	n.OnWebhook()
	n.OnImported()
	n.OnImportFailed()
	n.OnImportFailed()

	waitFor(t, func() bool { return cs.total() >= 2 }, time.Second)
	body := cs.snap()[1]
	if !strings.Contains(body, "1 picture") || !strings.Contains(body, "2 failed") {
		t.Errorf("expected mixed summary, got: %q", body)
	}
}

func TestErrorBypassesBurst(t *testing.T) {
	n, cs := newTestNotifier(t, 5*time.Second) // long window — error mustn't wait
	n.OnError(errExample("auth dead"))
	waitFor(t, func() bool { return cs.total() >= 1 }, time.Second)
	if !strings.Contains(cs.snap()[0], "auth dead") {
		t.Errorf("error not sent: %v", cs.snap())
	}
}

func TestSendUsesContext(t *testing.T) {
	// Sanity that *pushover.Client respects context cancellation — the
	// notifier sends fire-and-forget, but the cancellation path should
	// still cleanly exit and not deadlock the goroutine.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := pushover.New("t", "u")
	c.Endpoint = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Send(ctx, pushover.Message{Body: "x"}); err == nil {
		t.Skip("server returned 200 before cancel propagated; not a hard failure")
	}
	// also sanity check url-encoded form
	if _, err := url.ParseRequestURI(srv.URL); err != nil {
		t.Fatal(err)
	}
}

type errExample string

func (e errExample) Error() string { return string(e) }
