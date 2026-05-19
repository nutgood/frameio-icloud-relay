package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nutgood/frameio-icloud-relay/internal/pushover"
)

// notifier coalesces per-file events into "burst" Pushover messages so
// rapid-fire camera uploads don't generate one push per photo.
//
// Semantics:
//   - First webhook of a quiet period -> immediate "Received webhook,
//     importing photos" push, burst opens.
//   - Each import success / failure within the burst is counted but does
//     NOT send its own push.
//   - Each event (webhook arrival, import done) resets the idle timer.
//   - After idleWindow of silence the burst closes: one summary push
//     ("Imported N pictures" or "Imported N of M; K failed") is sent.
//   - OnError sends an immediate push regardless of burst state; these
//     are for non-transient problems the operator wants to see now
//     (auth failure, Photos.app missing).
//
// All public methods are safe to call from multiple goroutines.
type notifier struct {
	push       *pushover.Client
	idleWindow time.Duration

	mu             sync.Mutex
	open           bool
	startedAt      time.Time
	imported       int
	failed         int
	resetTimer     *time.Timer
	currentBurstID uint64
}

func newNotifier(push *pushover.Client, idleWindow time.Duration) *notifier {
	if idleWindow <= 0 {
		idleWindow = 30 * time.Second
	}
	return &notifier{push: push, idleWindow: idleWindow}
}

// OnWebhook is called from the webhook handler the moment Frame.io
// delivers a file.upload.completed event. Opens a burst if none is open.
func (n *notifier) OnWebhook() {
	n.mu.Lock()
	firstOfBurst := !n.open
	if firstOfBurst {
		n.open = true
		n.startedAt = time.Now()
		n.imported = 0
		n.failed = 0
		n.currentBurstID++
	}
	n.resetTimerLocked()
	n.mu.Unlock()

	if firstOfBurst {
		n.send(pushover.Message{
			Title: "Frame.io",
			Body:  "Received webhook, importing photos…",
		})
	}
}

// OnImported is called after a successful Photos.app import (and the
// subsequent Frame.io delete) for one file. Increments the burst counter;
// no immediate push.
func (n *notifier) OnImported() {
	n.mu.Lock()
	if !n.open {
		// A reconcile-driven import without a webhook (e.g. a missed
		// webhook caught by polling). Open a burst implicitly so the
		// operator still gets the summary.
		n.open = true
		n.startedAt = time.Now()
		n.imported = 0
		n.failed = 0
		n.currentBurstID++
	}
	n.imported++
	n.resetTimerLocked()
	n.mu.Unlock()
}

// OnImportFailed counts a failed import in the active burst (or opens
// one if none active). The failure summary is sent when the burst closes.
func (n *notifier) OnImportFailed() {
	n.mu.Lock()
	if !n.open {
		n.open = true
		n.startedAt = time.Now()
		n.imported = 0
		n.failed = 0
		n.currentBurstID++
	}
	n.failed++
	n.resetTimerLocked()
	n.mu.Unlock()
}

// OnError sends an immediate notification for an operator-visible
// problem (auth dead, Photos.app permission denied, etc.). Does not
// affect the burst counter.
func (n *notifier) OnError(err error) {
	if err == nil {
		return
	}
	n.send(pushover.Message{
		Title:    "Frame.io ⚠️",
		Body:     err.Error(),
		Priority: 1,
	})
}

// OnStartup announces the service starting up. Useful on a headless Mac
// mini to confirm the agent came back after a reboot. Single push.
func (n *notifier) OnStartup(authedAs string) {
	body := "Service started"
	if authedAs != "" {
		body += " (Frame.io: " + authedAs + ")"
	}
	n.send(pushover.Message{Title: "Frame.io", Body: body})
}

// snapshot returns the current burst state for the IPC status endpoint.
func (n *notifier) snapshot() (open bool, startedAt time.Time, imported, failed int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.open, n.startedAt, n.imported, n.failed
}

// resetTimerLocked re-arms the idle timer. Caller must hold n.mu.
func (n *notifier) resetTimerLocked() {
	if n.resetTimer != nil {
		n.resetTimer.Stop()
	}
	burstID := n.currentBurstID
	n.resetTimer = time.AfterFunc(n.idleWindow, func() {
		n.closeBurst(burstID)
	})
}

// closeBurst is called by the idle timer. The burstID guards against a
// stale timer firing after a new burst already opened on top of the old
// one (e.g. timer races a fresh webhook).
func (n *notifier) closeBurst(burstID uint64) {
	n.mu.Lock()
	if !n.open || n.currentBurstID != burstID {
		n.mu.Unlock()
		return
	}
	imported := n.imported
	failed := n.failed
	n.open = false
	n.mu.Unlock()

	var body string
	switch {
	case imported == 0 && failed == 0:
		body = "Burst closed: no files imported"
	case failed == 0:
		body = fmt.Sprintf("Imported %s", pluralize(imported, "picture"))
	case imported == 0:
		body = fmt.Sprintf("⚠️ %d import(s) failed", failed)
	default:
		body = fmt.Sprintf("Imported %s; %d failed", pluralize(imported, "picture"), failed)
	}
	n.send(pushover.Message{Title: "Frame.io", Body: body})
}

func (n *notifier) send(m pushover.Message) {
	if n.push == nil || !n.push.Enabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := n.push.Send(ctx, m); err != nil {
			log.Printf("pushover: %v", err)
		}
	}()
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
