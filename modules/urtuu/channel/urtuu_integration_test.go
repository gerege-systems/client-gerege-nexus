package channel

// Two installations, one process, one database.
//
// The pair is the arrangement the SSO federation documentation already uses to
// test a two-deployment protocol on one machine: each side gets its own signing
// key, its own organisation and its own HTTP server, and the only thing they
// share is the network between them. What is asserted here is the part that
// cannot be asserted in one process by inspection — that an invitation really
// establishes a link, that a signed envelope really survives the round trip,
// that a repeat really costs nothing, and that revocation really closes the
// door.
//
//	URTUU_TEST_DATABASE_URL=postgres://... go test ./internal/workspace/urtuu/...

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("URTUU_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("neither URTUU_TEST_DATABASE_URL nor DATABASE_URL is set")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	// The binding the platform's own pool installs, in the twenty lines of it
	// these tests need.
	//
	// internal/kernel/dbguard is what does this in production and a
	// distribution cannot import internal/, which is a fair rule and an
	// awkward one here: without any binding every query would run as the login
	// role, outside the row-level policies, and the tenant scoping these
	// handlers rely on would be tested for the wrong reason — every assertion
	// would pass because nothing was ever filtered.
	//
	// So the connection is bound the same way, from the same context value:
	// the acting organisation becomes app.current_tenant and the role becomes
	// the tenant role. What is deliberately not copied is the rest of the
	// guard — the operator role, the probe, the enable-after-migration
	// sequencing — none of which any test here is about.
	config.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		workspaceID, err := nexus.WorkspaceID(ctx)
		if err != nil || workspaceID == "" {
			// No organisation on this context: the login role, unbound. That is
			// what the platform does too — the exchange endpoints run before
			// anybody's session exists.
			_, err = conn.Exec(ctx, `SELECT set_config('role', 'none', false),
				set_config('app.current_tenant', '', false)`)
			return err == nil, err
		}
		_, err = conn.Exec(ctx, `SELECT set_config('role', $1, false),
			set_config('app.current_tenant', $2, false)`, tenantRole(ctx, conn), workspaceID)
		return err == nil, err
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// tenantRole is whichever of the two names this database calls the tenant role.
//
// Core 00079 renamed gerege_nexus_app to gerege_nexus_tenant and a cluster that
// has carried both — every developer's — keeps the old one, so naming either
// literally works on one machine and fails on the other. The same lookup the
// module's own migration does.
func tenantRole(ctx context.Context, conn *pgx.Conn) string {
	var role string
	if err := conn.QueryRow(ctx, `SELECT rolname FROM pg_roles
		 WHERE rolname IN ('gerege_nexus_tenant', 'gerege_nexus_app')
		 ORDER BY (rolname = 'gerege_nexus_tenant') DESC LIMIT 1`).Scan(&role); err != nil {
		return "none"
	}
	return role
}

// installation is one side of the pair: an organisation, a signing key and an
// address other installations can reach it at.
type installation struct {
	svc      *Service
	server   *httptest.Server
	tenantID string
}

func newInstallation(t *testing.T, pool *pgxpool.Pool, name string, seed byte) *installation {
	t.Helper()

	key := make([]byte, ed25519.SeedSize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	t.Setenv(signingKeyEnv, base64.StdEncoding.EncodeToString(key))
	service := New(pool, nil)
	if !service.Enabled() {
		t.Fatal("the installation has no Өртөө identity")
	}
	router := chi.NewRouter()
	router.Get("/.well-known/urtuu.json", service.HandleWellKnown)
	router.Post("/api/v1/urtuu/peers/redeem", service.HandleRedeem)
	router.Get("/api/v1/urtuu/exchange/pull", service.HandlePull)
	router.Post("/api/v1/urtuu/exchange/push", service.HandlePush)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	tenantID := uuid.NewString()
	slug := strings.ToLower(name) + "-" + tenantID[:8]
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO registry.tenants (id, name, slug) VALUES ($1, $2, $3)`,
		tenantID, "Өртөө test "+name, slug); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM registry.tenants WHERE id = $1`, tenantID)
	})

	return &installation{svc: service, server: server, tenantID: tenantID}
}

// adminCall runs one of the administrative handlers as a member of this
// installation's organisation, with no user attached — created_by is nullable
// and a real person would only add a row to clean up.
func (i *installation) adminCall(t *testing.T, handler http.HandlerFunc, body any, peerID string) *httptest.ResponseRecorder {
	t.Helper()

	encoded := []byte("{}")
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/urtuu/peers", strings.NewReader(string(encoded)))
	ctx := nexus.WithWorkspaceID(req.Context(), i.tenantID)
	if peerID != "" {
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("id", peerID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, routeCtx)
	}

	rec := httptest.NewRecorder()
	handler(rec, req.WithContext(ctx))
	return rec
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return value
}

// handshake runs the whole of §5's link establishment and returns the two rows'
// ids: the parent's view of the link and the child's.
func handshake(t *testing.T, parent, child *installation) (string, string) {
	t.Helper()

	invited := parent.adminCall(t, parent.svc.handleInvite, map[string]string{"name": "Ховд аймаг"}, "")
	if invited.Code != http.StatusCreated {
		t.Fatalf("invite = %d %s", invited.Code, invited.Body)
	}
	invitation := decodeJSON[struct {
		ID         string `json:"id"`
		InviteCode string `json:"invite_code"`
	}](t, invited)
	if invitation.InviteCode == "" {
		t.Fatal("the invitation carries no code")
	}

	joined := child.adminCall(t, child.svc.handleJoin, map[string]string{
		"invite_code": invitation.InviteCode,
		"base_url":    parent.server.URL,
		"name":        "Боловсролын яам",
	}, "")
	if joined.Code != http.StatusCreated {
		t.Fatalf("join = %d %s", joined.Code, joined.Body)
	}
	link := decodeJSON[struct {
		ID string `json:"id"`
	}](t, joined)

	return invitation.ID, link.ID
}

func (i *installation) peerStatus(t *testing.T, peerID string) string {
	t.Helper()
	var status string
	err := i.svc.db.QueryRow(nexus.WithWorkspaceID(context.Background(), i.tenantID),
		`SELECT status FROM urtuu_peers WHERE id = $1`, peerID).Scan(&status)
	if err != nil {
		t.Fatalf("read link: %v", err)
	}
	return status
}

func (i *installation) inboxCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := i.svc.db.QueryRow(nexus.WithWorkspaceID(context.Background(), i.tenantID),
		`SELECT count(*) FROM urtuu_inbox`).Scan(&count); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return count
}

func (i *installation) undelivered(t *testing.T) int {
	t.Helper()
	var count int
	if err := i.svc.db.QueryRow(nexus.WithWorkspaceID(context.Background(), i.tenantID),
		`SELECT count(*) FROM urtuu_deliveries WHERE delivered_at IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	return count
}

// childLink reads the child's own row back as the exchange loop would.
func (i *installation) childLink(t *testing.T, peerID string) peerRow {
	t.Helper()
	links, err := i.svc.activeChildLinks(context.Background())
	if err != nil {
		t.Fatalf("read links: %v", err)
	}
	for _, link := range links {
		if link.ID == peerID {
			return link
		}
	}
	t.Fatalf("link %s is not among the active child links", peerID)
	return peerRow{}
}

func TestAnInvitationEstablishesALinkBothSidesAgreeOn(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 1)
	child := newInstallation(t, pool, "child", 100)

	// Two installations, two identities. If these ever collided the cycle guard
	// in Ө3 would have nothing to compare against.
	if parent.svc.InstallationID() == child.svc.InstallationID() {
		t.Fatal("both installations claim the same identity")
	}

	parentPeerID, childPeerID := handshake(t, parent, child)

	// The child has everything it needs and says so; the parent is waiting for
	// its own administrator, exactly as report_grants makes a grantee wait.
	if got := child.peerStatus(t, childPeerID); got != "active" {
		t.Errorf("the child's link is %q, want active", got)
	}
	if got := parent.peerStatus(t, parentPeerID); got != "pending" {
		t.Errorf("the parent's link is %q, want pending — a link must not open on one side's say-so", got)
	}

	// And the keys were actually swapped, which is the only thing that makes a
	// signature checkable later.
	var storedKey string
	if err := pool.QueryRow(nexus.WithWorkspaceID(context.Background(), parent.tenantID),
		`SELECT peer_public_key FROM urtuu_peers WHERE id = $1`, parentPeerID).Scan(&storedKey); err != nil {
		t.Fatalf("read key: %v", err)
	}
	if storedKey != child.svc.PublicKey() {
		t.Error("the parent did not store the child's public key")
	}
}

func TestAnInvitationIsSpentOnce(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 2)
	child := newInstallation(t, pool, "child", 101)

	invited := parent.adminCall(t, parent.svc.handleInvite, map[string]string{"name": "Ховд"}, "")
	code := decodeJSON[struct {
		InviteCode string `json:"invite_code"`
	}](t, invited).InviteCode

	first := child.adminCall(t, child.svc.handleJoin, map[string]string{
		"invite_code": code, "base_url": parent.server.URL,
	}, "")
	if first.Code != http.StatusCreated {
		t.Fatalf("first join = %d %s", first.Code, first.Body)
	}

	// Same code, second time. A code that still worked would be a second way in
	// that nobody is watching.
	second := child.adminCall(t, child.svc.handleJoin, map[string]string{
		"invite_code": code, "base_url": parent.server.URL,
	}, "")
	if second.Code == http.StatusCreated {
		t.Fatal("an invitation was redeemed twice")
	}
}

func TestNothingMovesUntilTheParentConfirms(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 3)
	child := newInstallation(t, pool, "child", 102)

	parentPeerID, childPeerID := handshake(t, parent, child)
	link := child.childLink(t, childPeerID)

	if err := child.svc.exchangeOnce(context.Background(), link, 0); err == nil {
		t.Fatal("the channel carried traffic before the parent had confirmed the link")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want a 403 — an unconfirmed link is not an unknown one", err)
	}

	confirmed := parent.adminCall(t, parent.svc.handleConfirm, nil, parentPeerID)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", confirmed.Code, confirmed.Body)
	}
	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("exchange after confirmation: %v", err)
	}
}

// The whole point of the channel: work goes down, an answer comes back, and
// neither is trusted without its signature.
func TestWorkTravelsDownAndAnAnswerComesBack(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 4)
	child := newInstallation(t, pool, "child", 103)

	parentPeerID, childPeerID := handshake(t, parent, child)
	parent.adminCall(t, parent.svc.handleConfirm, nil, parentPeerID)
	link := child.childLink(t, childPeerID)

	messageID, err := parent.svc.Enqueue(context.Background(), parent.tenantID,
		contract.KindTaskAssigned, map[string]string{"code": "D-101", "title": "Хагас жилийн тооллого"},
		parentPeerID)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if got := child.inboxCount(t); got != 1 {
		t.Fatalf("the child holds %d envelopes, want 1", got)
	}

	// Still unsettled: the child has stored it but has not yet said so, and
	// until it does the parent must keep offering it. Marking a delivery done
	// when the response left is how a dropped answer becomes work that silently
	// never happened.
	if got := parent.undelivered(t); got != 1 {
		t.Errorf("the parent settled a delivery before it was acknowledged (%d outstanding)", got)
	}

	// Second round: the acknowledgement lands, and the same envelope arriving
	// again costs nothing.
	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("second exchange: %v", err)
	}
	if got := parent.undelivered(t); got != 0 {
		t.Errorf("the parent still holds %d undelivered envelopes after the acknowledgement", got)
	}
	if got := child.inboxCount(t); got != 1 {
		t.Errorf("the child holds %d envelopes after a repeat, want 1 — receipt is not идемпотент", got)
	}

	var storedID string
	if err := pool.QueryRow(nexus.WithWorkspaceID(context.Background(), child.tenantID),
		`SELECT message_id FROM urtuu_inbox LIMIT 1`).Scan(&storedID); err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if storedID != messageID {
		t.Errorf("message id = %q, want %q", storedID, messageID)
	}

	// And the answer, upwards.
	if _, err := child.svc.Enqueue(context.Background(), child.tenantID,
		contract.KindTaskUpdate, map[string]string{"status": "ACCEPTED"}, childPeerID); err != nil {
		t.Fatalf("enqueue upward: %v", err)
	}
	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("third exchange: %v", err)
	}
	if got := parent.inboxCount(t); got != 1 {
		t.Errorf("the parent holds %d envelopes, want the child's answer", got)
	}
	if got := child.undelivered(t); got != 0 {
		t.Errorf("the child still holds %d undelivered envelopes", got)
	}
}

// A token proves who is speaking. It does not prove who wrote what was said,
// and this is the test that says the difference is real: the row in the
// parent's own outbox is edited, so the transport is perfectly authentic and
// the content is not what was signed.
func TestAnEditedEnvelopeIsRefusedEvenOverAGoodLink(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 5)
	child := newInstallation(t, pool, "child", 104)

	parentPeerID, childPeerID := handshake(t, parent, child)
	parent.adminCall(t, parent.svc.handleConfirm, nil, parentPeerID)
	link := child.childLink(t, childPeerID)

	if _, err := parent.svc.Enqueue(context.Background(), parent.tenantID,
		contract.KindTaskAssigned, map[string]string{"code": "D-101"}, parentPeerID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := pool.Exec(nexus.WithWorkspaceID(context.Background(), parent.tenantID),
		`UPDATE urtuu_outbox SET payload = $1`, `{"code":"D-999"}`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if err := child.svc.exchangeOnce(context.Background(), link, 0); err == nil {
		t.Fatal("an edited envelope was accepted")
	}
	if got := child.inboxCount(t); got != 0 {
		t.Errorf("the child stored %d envelopes from an unverified batch", got)
	}
}

// Revocation is the control the whole two-sided consent exists for, so it has
// to be immediate and it has to be indistinguishable from never having existed.
func TestARevokedLinkStopsAnswering(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 6)
	child := newInstallation(t, pool, "child", 105)

	parentPeerID, childPeerID := handshake(t, parent, child)
	parent.adminCall(t, parent.svc.handleConfirm, nil, parentPeerID)
	link := child.childLink(t, childPeerID)
	if err := child.svc.exchangeOnce(context.Background(), link, 0); err != nil {
		t.Fatalf("the link did not work before it was revoked: %v", err)
	}

	revoked := parent.adminCall(t, parent.svc.handleRevoke, nil, parentPeerID)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", revoked.Code, revoked.Body)
	}

	err := child.svc.exchangeOnce(context.Background(), link, 0)
	if err == nil {
		t.Fatal("a revoked link still carried traffic")
	}
	// 401 and not 403: a revoked peer must not be able to tell itself apart
	// from one that was never there.
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want a 401", err)
	}

	// And the row is still here. "Who were we connected to, and when" outlives
	// the connection.
	var revokedAt *time.Time
	if err := pool.QueryRow(nexus.WithWorkspaceID(context.Background(), parent.tenantID),
		`SELECT revoked_at FROM urtuu_peers WHERE id = $1`, parentPeerID).Scan(&revokedAt); err != nil {
		t.Fatalf("the revoked link was deleted rather than closed: %v", err)
	}
	if revokedAt == nil {
		t.Error("the link is not marked revoked")
	}
}

// An installation with no key must not half-work: it publishes nothing and
// answers nothing, and the platform around it starts normally.
func TestAnInstallationWithoutAKeyIsSimplyOff(t *testing.T) {
	t.Setenv(signingKeyEnv, "")
	service := New(nil, nil)
	if service.Enabled() {
		t.Fatal("a service with no key reports itself enabled")
	}

	rec := httptest.NewRecorder()
	service.HandleWellKnown(rec, httptest.NewRequest(http.MethodGet, "/.well-known/urtuu.json", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("well-known = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	service.HandlePull(rec, httptest.NewRequest(http.MethodGet, "/api/v1/urtuu/exchange/pull", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("pull = %d, want 404", rec.Code)
	}
}

// A signature covers bytes, so the bytes have to come back.
//
// The stamp is written with nanoseconds in it deliberately. It used to be kept
// in a timestamptz column, which holds microseconds, so the string that came
// back out was not the string that had been signed — and every envelope this
// platform sent failed verification at the far end. It went unnoticed on a
// developer's Mac, where the clock usually stops at microseconds, and turned
// every Өртөө test red on Linux, where it does not.
//
// The instant here is fixed rather than taken from the clock, so this fails on
// the old schema whatever machine it runs on.
func TestASignedEnvelopeSurvivesTheOutboxToTheNanosecond(t *testing.T) {
	pool := openPool(t)
	t.Setenv(insecurePeersEnv, "1")
	parent := newInstallation(t, pool, "parent", 40)
	child := newInstallation(t, pool, "child", 140)

	parentPeerID, _ := handshake(t, parent, child)
	if rec := parent.adminCall(t, parent.svc.handleConfirm, nil, parentPeerID); rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body)
	}

	created := time.Date(2026, 8, 16, 9, 0, 0, 123456789, time.UTC)
	envelope, err := contract.New(uuid.NewString(), contract.KindTaskAssigned, created,
		map[string]string{"code": "D-101"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasSuffix(envelope.CreatedAt, "123456789Z") {
		t.Fatalf("the test did not build a nanosecond stamp: %q", envelope.CreatedAt)
	}
	envelope, err = contract.Sign(parent.svc.signing, envelope)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	ctx := nexus.WithWorkspaceID(context.Background(), parent.tenantID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := parent.svc.queue(ctx, tx, parent.tenantID, envelope, []string{parentPeerID}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	sent, err := parent.svc.dueEnvelopes(context.Background(),
		peerRow{ID: parentPeerID, TenantID: parent.tenantID}, 10)
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("the queue holds %d envelopes, want 1", len(sent))
	}
	if sent[0].CreatedAt != envelope.CreatedAt {
		t.Errorf("created_at came back as %q, was signed as %q", sent[0].CreatedAt, envelope.CreatedAt)
	}
	// The assertion the far end makes, made here.
	if err := contract.Verify(parent.svc.public, sent[0]); err != nil {
		t.Errorf("the envelope this platform is about to send does not verify: %v", err)
	}
}
