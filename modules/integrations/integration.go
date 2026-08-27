/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package integration connects a tenant to systems outside the platform:
 * webhook subscribers, government gateways, and the SaaS providers a signed
 * document or a scheduled appointment has to reach — Google Drive, Dropbox and
 * Google Meet.
 */

package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/google/uuid"
)

// EventPayload is the body a webhook subscriber receives.
type EventPayload struct {
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	TenantID  string         `json:"tenant_id"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

// Manager is the tenant-scoped connector registry.
//
// It used to be a process-global map with no tenant column: every tenant
// administrator listed every other tenant's connectors — target URLs and, until
// recently, their signing secrets — and a dispatched event went to all of them
// regardless of whose event it was. Every method here takes a tenantID, and no
// query runs without it.
type Manager struct {
	store      *store
	httpClient *http.Client

	// deliverySlots bounds how many webhook deliveries are in flight for the
	// whole process. Dispatch used to start one goroutine per subscriber with
	// nothing holding them back, so a tenant with two hundred subscribers — or
	// a burst of events across tenants — opened as many sockets at once as the
	// numbers happened to multiply out to.
	deliverySlots chan struct{}

	// refreshLocks serialises token renewal per connector, keyed by connector
	// id. See refreshAccess: two exports starting together otherwise both
	// refreshed the same grant and raced to store the result.
	refreshLocks sync.Map
}

func NewManager(db nexus.DB) *Manager {
	return &Manager{
		store: &store{db: db},
		// Uploading a signed PDF is not a 10-second operation over a bad link,
		// so this is looser than the old webhook-only client. Individual calls
		// still carry their own context deadline.
		httpClient:    newHTTPClient(60 * time.Second),
		deliverySlots: make(chan struct{}, maxConcurrentDeliveries),
	}
}

// List returns the tenant's connectors. Secrets are structurally absent from
// Connector, so there is nothing to redact here.
func (m *Manager) List(ctx context.Context, tenantID string) ([]*Connector, error) {
	return m.store.list(ctx, tenantID)
}

// Get returns one connector belonging to the tenant.
func (m *Manager) Get(ctx context.Context, tenantID, id string) (*Connector, error) {
	return m.store.get(ctx, tenantID, id)
}

// Deliveries returns what has recently left the platform for this tenant.
func (m *Manager) Deliveries(ctx context.Context, tenantID string, limit int) ([]Delivery, error) {
	return m.store.deliveries(ctx, tenantID, limit)
}

// Create registers a connector for one tenant.
func (m *Manager) Create(ctx context.Context, tenantID string, req SaveRequest) (*Connector, error) {
	if err := validate(&req); err != nil {
		return nil, err
	}
	return m.store.create(ctx, tenantID, req)
}

// Update edits a connector. An OAuth connector's grant is untouched: it changes
// by connecting or disconnecting, never by editing the form.
func (m *Manager) Update(ctx context.Context, tenantID, id string, req SaveRequest) (*Connector, error) {
	existing, err := m.store.get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	// The provider decides which credentials and capabilities a row has, so it
	// is fixed at creation: changing it would leave a Dropbox grant attached to
	// a connector claiming to be a webhook.
	req.Provider = existing.Provider
	if req.Status == "" {
		req.Status = existing.Status
	}
	if err := validate(&req); err != nil {
		return nil, err
	}
	return m.store.update(ctx, tenantID, id, req)
}

// Delete removes a connector and, by cascade, its delivery history.
func (m *Manager) Delete(ctx context.Context, tenantID, id string) error {
	return m.store.delete(ctx, tenantID, id)
}

// Disconnect drops a stored OAuth grant without deleting the connector, so the
// configuration survives re-authorising a different account.
func (m *Manager) Disconnect(ctx context.Context, tenantID, id string) error {
	return m.store.disconnect(ctx, tenantID, id)
}

// InvalidError is a request the person who submitted it can fix by submitting
// something else: a name they left blank, a provider that does not exist, a URL
// pointing at the deployment's own network.
//
// It exists so the API layer can tell those apart from a database that is
// down. Everything used to arrive there as a bare error and come back as a 400
// carrying its text, which answered an outage with "bad request" and handed the
// browser whatever the driver happened to say.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

// validate normalises and checks a save request. It runs before both create and
// update so the two cannot drift apart.
func validate(req *SaveRequest) error {
	spec, err := SpecFor(req.Provider)
	if err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if req.Name == "" {
		return invalid("a connector needs a name")
	}
	if len(req.Name) > 255 {
		return invalid("the connector name is too long")
	}
	if req.Config == nil {
		req.Config = map[string]string{}
	}

	// Status is the administrator's intent, so only the two values that express
	// one are accepted. Checking it here rather than letting the CHECK
	// constraint reject it turns a raw Postgres message returned to the browser
	// into a sentence about the field that was wrong.
	req.Status = ConnectorStatus(strings.ToUpper(strings.TrimSpace(string(req.Status))))
	switch req.Status {
	case "":
		req.Status = StatusActive
	case StatusActive, StatusInactive:
	default:
		return invalid("status must be %s or %s", StatusActive, StatusInactive)
	}

	if spec.OAuth {
		// The destination is an account reached through a grant — there is no
		// URL for an operator to type, and accepting one would suggest the
		// upload goes somewhere it does not.
		req.TargetURL = ""
		req.Secret = ""
		if _, _, err := spec.Credentials(); err != nil {
			return err
		}
		if !EncryptionConfigured() {
			return ErrNoEncryptionKey
		}
		return nil
	}

	if req.TargetURL == "" {
		return invalid("a %s connector needs a target URL", spec.Provider)
	}
	parsed, err := url.Parse(req.TargetURL)
	if err != nil || parsed.Host == "" {
		return invalid("the target URL is not a valid absolute URL")
	}
	// The server makes the outbound request, so whoever can save a connector
	// chooses where this process connects — and a tenant administrator is not
	// the operator of the deployment. The scheme keeps that on ordinary web
	// protocols; checkTargetHost keeps it off the deployment's own network. The
	// binding check is at dial time (see network.go), because a hostname
	// resolves later and can resolve elsewhere.
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return invalid("the target URL must be http or https")
	}
	if err := checkTargetHost(parsed.Hostname()); err != nil {
		return err
	}
	if req.Secret != "" && !EncryptionConfigured() {
		return ErrNoEncryptionKey
	}
	return nil
}

// ─── Webhook dispatch ────────────────────────────────────────────────────────

// dispatchTimeout bounds one attempt at a webhook delivery. Delivery does not
// inherit the caller's cancellation (see DispatchEvent), so it needs a deadline
// of its own.
const dispatchTimeout = 15 * time.Second

// maxConcurrentDeliveries caps outbound webhook calls across the process, and
// deliveryQueueWait is how long a delivery waits for a turn before giving up.
//
// Waiting forever would be the unbounded goroutine problem wearing a different
// coat: under a burst the parked deliveries would pile up invisibly. Giving up
// puts the overload in the delivery log, where it can be read.
const (
	maxConcurrentDeliveries = 8
	deliveryQueueWait       = 30 * time.Second
)

// deliveryBackoff is the wait before each retry, so a delivery is attempted
// len(deliveryBackoff)+1 times.
//
// A subscriber that is restarting, rate-limiting or briefly unreachable is the
// ordinary case, and dropping the event on the first refusal is what made a
// deployment feel unreliable. Retries are safe for the subscriber to absorb
// because every attempt carries the same EventID, which is what a receiver
// deduplicates on.
//
// It is a variable so the tests can exercise the retry path without waiting
// twelve seconds for it.
var deliveryBackoff = []time.Duration{2 * time.Second, 10 * time.Second}

// DispatchEvent posts an event to the tenant's active webhook subscribers.
//
// The tenantID argument is the fix for what this used to be: the old version
// read a process-global map and posted one tenant's events to every tenant's
// subscribers.
func (m *Manager) DispatchEvent(ctx context.Context, tenantID string, payload EventPayload) error {
	if payload.TenantID == "" {
		payload.TenantID = tenantID
	}
	if payload.TenantID != tenantID {
		return fmt.Errorf("event tenant %q does not match the dispatching tenant", payload.TenantID)
	}
	// Retries resend the same body, so the event needs an identity a subscriber
	// can deduplicate on. An unstamped event would arrive as two different
	// events on the retry, which is worse than the failure being retried.
	if payload.EventID == "" {
		payload.EventID = uuid.NewString()
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal event payload: %w", err)
	}

	targets, err := m.store.dispatchTargets(ctx, tenantID)
	if err != nil {
		return err
	}

	// Delivery is asynchronous, so it must not carry the caller's cancellation.
	// ctx is typically a request context: it is cancelled the moment the handler
	// that called DispatchEvent returns, which is almost always before the POST
	// has left the process — so every webhook raced its own cancellation and
	// most were never delivered. WithoutCancel keeps the trace and request
	// values that make the outbound call attributable and drops only the
	// cancellation; the timeout above supplies the deadline instead.
	sendCtx := context.WithoutCancel(ctx)

	for _, t := range targets {
		target := t
		background("integration-delivery", func() { m.deliver(sendCtx, tenantID, target, payload, bodyBytes) })
	}

	return nil
}

// deliver sends one event to one subscriber, retrying what is worth retrying.
//
// The outcome is written once, at the end, so the delivery log holds one row
// per event per subscriber rather than one row per attempt — the log is read to
// answer "did this reach them", and three rows for one event does not answer it.
func (m *Manager) deliver(ctx context.Context, tenantID string, t dispatchTarget,
	payload EventPayload, body []byte) {

	select {
	case m.deliverySlots <- struct{}{}:
		defer func() { <-m.deliverySlots }()
	case <-time.After(deliveryQueueWait):
		const detail = "gave up waiting for an outbound delivery slot"
		m.store.noteError(ctx, tenantID, t.id, detail)
		m.store.recordDelivery(ctx, tenantID, t.id, "event", payload.EventType, "FAILED", detail, "", "")
		return
	}

	var detail string
	attempts := len(deliveryBackoff) + 1
	for attempt := range attempts {
		if attempt > 0 {
			// The name is a constant, not the subscriber's URL: that is a value
			// a tenant types in, and a Prometheus label taken from one is
			// unbounded cardinality driven by user input.

			select {
			case <-time.After(deliveryBackoff[attempt-1]):
			case <-ctx.Done():
				detail = fmt.Sprintf("%s (gave up after %d attempts)", detail, attempt)
				m.store.noteError(ctx, tenantID, t.id, detail)
				m.store.recordDelivery(ctx, tenantID, t.id, "event", payload.EventType,
					"FAILED", detail, "", "")
				return
			}
		}

		retryable, failure := m.attemptDelivery(ctx, t, payload, body)
		if failure == "" {
			m.store.noteSuccess(ctx, tenantID, t.id)
			m.store.recordDelivery(ctx, tenantID, t.id, "event", payload.EventType,
				"OK", deliveredAfter(attempt), "", "")
			return
		}
		detail = failure
		// A subscriber that answered 404 or 401 will answer the same way in ten
		// seconds. Retrying it is noise for them and a delay for the report.
		if !retryable {
			break
		}
	}

	m.store.noteError(ctx, tenantID, t.id, detail)
	m.store.recordDelivery(ctx, tenantID, t.id, "event", payload.EventType, "FAILED", detail, "", "")
}

// attemptDelivery makes one POST. It returns whether the failure is worth
// another attempt, and the failure itself — empty when the subscriber accepted
// the event.
func (m *Manager) attemptDelivery(ctx context.Context, t dispatchTarget,
	payload EventPayload, body []byte) (retryable bool, failure string) {

	reqCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		// A URL this malformed will not improve on a second reading.
		return false, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	// X-ERP-* are legacy compatibility header names, kept through the Gerege
	// Nexus rebrand. Subscribers read these exact names and verify the
	// signature against them; renaming would break every existing endpoint
	// silently. A Nexus-named alias would need dual-emission and a deprecation
	// window, not a rename.
	req.Header.Set("X-ERP-Event", payload.EventType)

	if t.secret != "" {
		mac := hmac.New(sha256.New, []byte(t.secret))
		mac.Write(body)
		req.Header.Set("X-ERP-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		// Refused, reset, timed out, DNS not answering yet — all of which are
		// routinely temporary. The exception is the network guard: an address
		// inside the deployment will still be inside it on the next attempt.
		return !strings.Contains(err.Error(), "refused to connect to"), err.Error()
	}
	// Drain before closing so the connection returns to the pool instead of
	// being torn down after every event.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()

	if resp.StatusCode >= 300 {
		return retryableStatus(resp.StatusCode), fmt.Sprintf("subscriber answered %d", resp.StatusCode)
	}
	return false, ""
}

// retryableStatus reports whether a status says "not now" rather than "no".
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

func deliveredAfter(attempt int) string {
	if attempt == 0 {
		return ""
	}
	return fmt.Sprintf("delivered on attempt %d", attempt+1)
}

// ─── Capability dispatch ─────────────────────────────────────────────────────

// ExportResult describes where a file ended up.
type ExportResult struct {
	IntegrationID   string `json:"integration_id"`
	IntegrationName string `json:"integration_name"`
	Provider        string `json:"provider"`
	ExternalID      string `json:"external_id,omitempty"`
	URL             string `json:"url,omitempty"`
}

// ExportFile stores a file in one connector belonging to the tenant.
func (m *Manager) ExportFile(ctx context.Context, tenantID, integrationID, filename string,
	content []byte, reference string) (*ExportResult, error) {

	conn, err := m.store.get(ctx, tenantID, integrationID)
	if err != nil {
		return nil, err
	}
	spec, err := SpecFor(conn.Provider)
	if err != nil {
		return nil, err
	}
	if !spec.Supports(CapabilityFileExport) {
		return nil, fmt.Errorf("%s cannot store files", conn.Provider)
	}
	if conn.Status != StatusActive {
		return nil, fmt.Errorf("connector %q is not active", conn.Name)
	}

	token, err := m.accessToken(ctx, tenantID, conn, spec)
	if err != nil {
		return nil, err
	}

	upload := func(token string) (*ExportResult, error) {
		switch conn.Provider {
		case ProviderGoogleDrive:
			return m.driveUpload(ctx, token, conn, filename, content)
		case ProviderDropbox:
			return m.dropboxUpload(ctx, token, conn, filename, content)
		}
		return nil, fmt.Errorf("no file export implementation for %s", conn.Provider)
	}

	res, err := upload(token)
	// A token can stop working before it was due to. One refresh and one retry
	// turns that into a delivery instead of an administrator wondering why a
	// connector that says Connected refuses to file anything.
	if fresh, retry := m.tokenAfterRejection(ctx, tenantID, conn, spec, token, err); retry {
		res, err = upload(fresh)
	}
	if err != nil {
		m.store.noteError(ctx, tenantID, conn.ID, err.Error())
		m.store.recordDelivery(ctx, tenantID, conn.ID, "file", reference, "FAILED", err.Error(), "", "")
		return nil, err
	}

	res.IntegrationID, res.IntegrationName, res.Provider = conn.ID, conn.Name, string(conn.Provider)
	m.store.noteSuccess(ctx, tenantID, conn.ID)
	m.store.recordDelivery(ctx, tenantID, conn.ID, "file", reference, "OK", "", res.ExternalID, res.URL)
	return res, nil
}

// ExportFileToAll stores a file in every connector the tenant has marked for
// automatic export. One failing destination must not stop the others, so
// failures are recorded against their own connector and skipped.
func (m *Manager) ExportFileToAll(ctx context.Context, tenantID, filename string,
	content []byte, reference string) []ExportResult {

	conns, err := m.store.list(ctx, tenantID)
	if err != nil {
		slog.Warn("integration: could not list export destinations", "tenant", tenantID, "error", err)
		return nil
	}
	results := make([]ExportResult, 0)
	for _, conn := range conns {
		spec, err := SpecFor(conn.Provider)
		if err != nil || !spec.Supports(CapabilityFileExport) {
			continue
		}
		if conn.Status != StatusActive || !conn.Connected || conn.Config["auto_export"] != "true" {
			continue
		}
		res, err := m.ExportFile(ctx, tenantID, conn.ID, filename, content, reference)
		if err != nil {
			slog.Warn("integration: automatic export failed",
				"tenant", tenantID, "connector", conn.Name, "error", err)
			continue
		}
		results = append(results, *res)
	}
	return results
}

// Meeting is a conferencing link created for a scheduled event.
type Meeting struct {
	IntegrationID   string `json:"integration_id"`
	IntegrationName string `json:"integration_name"`
	Provider        string `json:"provider"`
	JoinURL         string `json:"join_url"`
	ExternalID      string `json:"external_id,omitempty"`
}

// CreateMeeting books a conferencing link on one connector.
func (m *Manager) CreateMeeting(ctx context.Context, tenantID, integrationID, title string,
	startsAt time.Time, duration time.Duration, reference string) (*Meeting, error) {

	conn, err := m.store.get(ctx, tenantID, integrationID)
	if err != nil {
		return nil, err
	}
	spec, err := SpecFor(conn.Provider)
	if err != nil {
		return nil, err
	}
	if !spec.Supports(CapabilityMeeting) {
		return nil, fmt.Errorf("%s cannot create meetings", conn.Provider)
	}
	if conn.Status != StatusActive {
		return nil, fmt.Errorf("connector %q is not active", conn.Name)
	}

	token, err := m.accessToken(ctx, tenantID, conn, spec)
	if err != nil {
		return nil, err
	}

	book := func(token string) (*Meeting, error) {
		return m.googleMeetCreate(ctx, token, conn, title, startsAt, duration)
	}
	meeting, err := book(token)
	if fresh, retry := m.tokenAfterRejection(ctx, tenantID, conn, spec, token, err); retry {
		meeting, err = book(fresh)
	}
	if err != nil {
		m.store.noteError(ctx, tenantID, conn.ID, err.Error())
		m.store.recordDelivery(ctx, tenantID, conn.ID, "meeting", reference, "FAILED", err.Error(), "", "")
		return nil, err
	}

	meeting.IntegrationID, meeting.IntegrationName = conn.ID, conn.Name
	meeting.Provider = string(conn.Provider)
	m.store.noteSuccess(ctx, tenantID, conn.ID)
	m.store.recordDelivery(ctx, tenantID, conn.ID, "meeting", reference, "OK", "",
		meeting.ExternalID, meeting.JoinURL)
	return meeting, nil
}

// FirstMeetingConnector returns the tenant's connected meeting provider, if it
// has one. Booking an online appointment should not make the caller name a
// connector when there is only ever one plausible answer.
func (m *Manager) FirstMeetingConnector(ctx context.Context, tenantID string) (*Connector, error) {
	conns, err := m.store.list(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, conn := range conns {
		spec, err := SpecFor(conn.Provider)
		if err != nil || !spec.Supports(CapabilityMeeting) {
			continue
		}
		if conn.Status == StatusActive && conn.Connected {
			return conn, nil
		}
	}
	return nil, ErrNotFound
}
