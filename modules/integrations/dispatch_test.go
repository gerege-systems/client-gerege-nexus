package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortBackoff makes the retry path testable. The waits are the point of the
// feature, not of the test.
func shortBackoff(t *testing.T) {
	t.Helper()
	original := deliveryBackoff
	deliveryBackoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { deliveryBackoff = original })
}

// waitForDeliveries blocks until the delivery log holds what the test is
// waiting for. Dispatch is asynchronous by design — the caller's request must
// not wait on someone else's server — so the log is where the outcome appears.
func waitForDeliveries(t *testing.T, m *Manager, tenantID string, want int) []Delivery {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, err := m.Deliveries(context.Background(), tenantID, 100)
		if err != nil {
			t.Fatalf("deliveries: %v", err)
		}
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d deliveries were recorded within the timeout", len(got), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A subscriber that is restarting answers 503 and then answers properly. The
// event used to be dropped on that first refusal, with nothing to resend it:
// one bad moment on the receiving end lost the event permanently.
func TestATransientFailureIsRetriedUntilItSucceeds(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "retry-test-key")
	t.Setenv(allowPrivateEnv, "true") // the test subscriber listens on loopback
	resetKeyForTest()
	shortBackoff(t)

	var attempts atomic.Int32
	var eventIDs sync.Map
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		body := make([]byte, 2048)
		read, _ := r.Body.Read(body)
		eventIDs.Store(string(body[:read]), true)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	if _, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderWebhook, Name: "Restarting subscriber", TargetURL: server.URL,
		Secret: "signing-secret",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.DispatchEvent(ctx, tenantID, EventPayload{EventType: "contact.created"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	deliveries := waitForDeliveries(t, m, tenantID, 1)
	if len(deliveries) != 1 {
		t.Fatalf("one event produced %d log entries, want 1 — the log answers "+
			"'did this reach them', not 'how many times did we try'", len(deliveries))
	}
	if deliveries[0].Outcome != "OK" {
		t.Fatalf("outcome is %s (%s), want OK", deliveries[0].Outcome, deliveries[0].Detail)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("the subscriber was called %d times, want 3", got)
	}

	// Every attempt carries the same event, so a subscriber that received the
	// 503'd attempts anyway can deduplicate them.
	count := 0
	eventIDs.Range(func(_, _ any) bool { count++; return true })
	if count != 1 {
		t.Fatalf("the retries sent %d different bodies — a subscriber cannot deduplicate those", count)
	}

	// And the connector is neither switched off nor left carrying the failure
	// it recovered from.
	conns, err := m.List(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if conns[0].Status != StatusActive || conns[0].LastError != "" {
		t.Fatalf("after a recovered delivery the connector is %s / %q",
			conns[0].Status, conns[0].LastError)
	}
}

// A subscriber that answers 400 has read the event and refused it. Sending it
// again is noise for them and a delay before the administrator is told.
func TestARejectedEventIsNotRetried(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "no-retry-test-key")
	t.Setenv(allowPrivateEnv, "true")
	resetKeyForTest()
	shortBackoff(t)

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderWebhook, Name: "Fussy subscriber", TargetURL: server.URL,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.DispatchEvent(ctx, tenantID, EventPayload{EventType: "contact.created"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	deliveries := waitForDeliveries(t, m, tenantID, 1)
	if deliveries[0].Outcome != "FAILED" {
		t.Fatalf("outcome is %s, want FAILED", deliveries[0].Outcome)
	}
	if !strings.Contains(deliveries[0].Detail, "400") {
		t.Fatalf("the recorded reason %q does not say what the subscriber answered", deliveries[0].Detail)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("a rejected event was sent %d times, want 1", got)
	}

	// The failure is reported against the connector and the connector is still
	// switched on — the next event is still attempted.
	conn, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if conn.LastError == "" {
		t.Fatal("the failure was not recorded against the connector")
	}
	if conn.Status != StatusActive {
		t.Fatalf("a rejected event switched the connector to %s", conn.Status)
	}
}

// Dispatch used to start one goroutine per subscriber with nothing holding
// them back: a tenant with two hundred subscribers opened two hundred sockets
// at once, and a burst of events multiplied that out.
func TestConcurrentDeliveriesAreCapped(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "concurrency-test-key")
	t.Setenv(allowPrivateEnv, "true")
	resetKeyForTest()
	shortBackoff(t)

	var inFlight, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	const subscribers = maxConcurrentDeliveries + 6
	for i := range subscribers {
		if _, err := m.Create(ctx, tenantID, SaveRequest{
			Provider:  ProviderWebhook,
			Name:      fmt.Sprintf("Subscriber %d", i),
			TargetURL: server.URL,
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	if err := m.DispatchEvent(ctx, tenantID, EventPayload{EventType: "contact.created"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	deliveries := waitForDeliveries(t, m, tenantID, subscribers)
	for _, d := range deliveries {
		if d.Outcome != "OK" {
			t.Fatalf("a delivery failed under the cap: %s", d.Detail)
		}
	}
	if got := peak.Load(); got > maxConcurrentDeliveries {
		t.Fatalf("%d deliveries were in flight at once, cap is %d", got, maxConcurrentDeliveries)
	}
	// The cap must bound the work, not serialise it: every subscriber still
	// gets the event, and they do not queue up one at a time.
	if peak.Load() < 2 {
		t.Fatalf("deliveries never overlapped (peak %d) — the cap turned into a queue", peak.Load())
	}
}
