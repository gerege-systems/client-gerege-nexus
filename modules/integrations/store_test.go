package integrations

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The whole point of this table is that a connector belongs to one tenant.
// Proving that needs the real schema — the tenant column, the foreign keys and
// the uniqueness constraint are what enforce it — so these run against a
// migrated throwaway database:
//
//	INTEGRATION_TEST_DATABASE_URL=postgres://... go test ./internal/workspace/integration/...
//
// CI sets DATABASE_URL for the test job, which is picked up as a fallback, so
// this runs there without extra configuration.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("INTEGRATION_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set INTEGRATION_TEST_DATABASE_URL or DATABASE_URL to a migrated test database")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newTenant creates a throwaway tenant and removes it — and everything that
// cascades from it — when the test finishes.
func newTenant(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	slug := "itest-" + id[:8]
	if _, err := pool.Exec(ctx,
		`INSERT INTO registry.tenants (id, slug, name) VALUES ($1, $2, $3)`, id, slug, "Integration test"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM registry.tenants WHERE id = $1`, id)
	})
	return id
}

// A connector belongs to one tenant. This is the defect the table exists to
// fix: the registry used to be a process-global map, so every tenant
// administrator listed — and could dispatch to — every other tenant's
// connectors.
func TestConnectorsAreInvisibleToOtherTenants(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "tenant-isolation-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()

	alice := newTenant(t, pool)
	bob := newTenant(t, pool)

	created, err := m.Create(ctx, alice, SaveRequest{
		Provider:  ProviderWebhook,
		Name:      "Alice's subscriber",
		TargetURL: "https://alice.example.mn/hook",
		Secret:    "alice-signing-secret",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Bob's list must not contain it.
	bobList, err := m.List(ctx, bob)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, conn := range bobList {
		if conn.ID == created.ID {
			t.Fatal("a connector created by one tenant appeared in another tenant's list")
		}
	}

	// Nor may Bob reach it by id. It reads as missing rather than forbidden:
	// the distinction would confirm the id exists.
	if _, err := m.Get(ctx, bob, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Get returned %v, want ErrNotFound", err)
	}
	if err := m.Delete(ctx, bob, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Delete returned %v, want ErrNotFound", err)
	}
	if err := m.Disconnect(ctx, bob, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Disconnect returned %v, want ErrNotFound", err)
	}
	if _, err := m.Update(ctx, bob, created.ID, SaveRequest{
		Name: "renamed by Bob", TargetURL: "https://bob.example.mn/hook",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Update returned %v, want ErrNotFound", err)
	}

	// And Alice's connector still says what Alice said.
	still, err := m.Get(ctx, alice, created.ID)
	if err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	if still.Name != "Alice's subscriber" {
		t.Fatalf("connector name is now %q", still.Name)
	}
}

// A signing secret goes in and does not come back. The API returns Connector,
// which has no field for it, so the redaction is structural rather than a step
// somebody has to remember on each new endpoint.
func TestTheStoredSecretNeverLeavesThePackage(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "secret-redaction-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	const secret = "a-signing-secret-nobody-should-read-back"
	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderWebhook, Name: "Signed", TargetURL: "https://x.example.mn/hook", Secret: secret,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// It is usable internally — dispatch needs it to sign.
	got, err := m.store.secretFor(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("secretFor: %v", err)
	}
	if got != secret {
		t.Fatalf("secretFor gave %q", got)
	}

	// And it is not in the ciphertext column in readable form.
	var raw []byte
	if err := pool.QueryRow(ctx,
		`SELECT secret_ciphertext FROM workspace.integrations WHERE id = $1`, created.ID).Scan(&raw); err != nil {
		t.Fatalf("read ciphertext: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("no ciphertext was stored")
	}
	if string(raw) == secret {
		t.Fatal("the secret is stored in the clear")
	}
}

// An update that leaves the secret blank means "unchanged". Treating blank as
// "clear it" would silently unsign a tenant's webhooks the first time somebody
// edited the connector's name.
func TestUpdatingWithoutASecretKeepsTheStoredOne(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "secret-preservation-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderWebhook, Name: "Before", TargetURL: "https://x.example.mn/hook", Secret: "keep-me",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := m.Update(ctx, tenantID, created.ID, SaveRequest{
		Name: "After", TargetURL: "https://x.example.mn/hook", Status: StatusActive,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	secret, err := m.store.secretFor(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("secretFor: %v", err)
	}
	if secret != "keep-me" {
		t.Fatalf("the secret is now %q — an edit dropped it", secret)
	}
}

// Dispatch targets are read per tenant. A webhook belonging to one tenant must
// never be a destination for another's events.
func TestDispatchTargetsAreScopedToTheTenant(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "dispatch-scope-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()

	alice := newTenant(t, pool)
	bob := newTenant(t, pool)

	if _, err := m.Create(ctx, alice, SaveRequest{
		Provider: ProviderWebhook, Name: "Alice hook", TargetURL: "https://alice.example.mn/hook",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	bobTargets, err := m.store.dispatchTargets(ctx, bob)
	if err != nil {
		t.Fatalf("dispatchTargets: %v", err)
	}
	for _, target := range bobTargets {
		if target.url == "https://alice.example.mn/hook" {
			t.Fatal("one tenant's event would be delivered to another tenant's subscriber")
		}
	}

	aliceTargets, err := m.store.dispatchTargets(ctx, alice)
	if err != nil {
		t.Fatalf("dispatchTargets: %v", err)
	}
	if len(aliceTargets) != 1 {
		t.Fatalf("the owning tenant has %d targets, want 1", len(aliceTargets))
	}
}

// DispatchEvent refuses a payload stamped with a different tenant than the one
// dispatching, so a caller cannot address someone else's subscribers by
// setting the field.
func TestDispatchRefusesAMismatchedTenant(t *testing.T) {
	pool := testPool(t)
	m := NewManager(pool)

	err := m.DispatchEvent(context.Background(), "11111111-1111-1111-1111-111111111111",
		EventPayload{TenantID: "22222222-2222-2222-2222-222222222222", EventType: "contact.created"})
	if err == nil {
		t.Fatal("dispatch accepted an event stamped with another tenant")
	}
}

// Two connectors with one name is how a signed document gets filed into the
// wrong account, so the name is unique per tenant — and only per tenant.
func TestNamesAreUniquePerTenantAndNotGlobally(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "uniqueness-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()

	alice := newTenant(t, pool)
	bob := newTenant(t, pool)

	req := SaveRequest{Provider: ProviderWebhook, Name: "Archive", TargetURL: "https://x.example.mn/hook"}
	if _, err := m.Create(ctx, alice, req); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := m.Create(ctx, alice, req); err == nil {
		t.Fatal("the same tenant registered two connectors called Archive")
	}
	// Bob may still call his "Archive" — the constraint is per tenant.
	if _, err := m.Create(ctx, bob, req); err != nil {
		t.Fatalf("another tenant could not reuse the name: %v", err)
	}
}

// A failed delivery must not switch the connector off.
//
// noteError used to write status = 'ERROR', and every selection query requires
// ACTIVE — so a subscriber that answered 503 once was dropped from
// dispatchTargets permanently. Nothing contacted it again, which meant the
// success that would have cleared the flag could never happen: one bad minute
// silently unsubscribed a tenant until an administrator noticed and re-saved
// the row. The failure is now recorded and the connector is still a target.
func TestAFailedDeliveryDoesNotSwitchTheConnectorOff(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "failure-does-not-disable-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderWebhook, Name: "Flaky subscriber", TargetURL: "https://flaky.example.mn/hook",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	m.store.noteError(ctx, tenantID, created.ID, "subscriber answered 503")

	after, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != StatusActive {
		t.Fatalf("a failed delivery left the connector %s, want %s", after.Status, StatusActive)
	}
	if after.LastError != "subscriber answered 503" {
		t.Fatalf("the failure was not recorded: last_error is %q", after.LastError)
	}

	// The regression itself: it is still somewhere the next event goes.
	targets, err := m.store.dispatchTargets(ctx, tenantID)
	if err != nil {
		t.Fatalf("dispatchTargets: %v", err)
	}
	found := false
	for _, target := range targets {
		if target.id == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("a subscriber that failed once is no longer sent events, so it can never recover")
	}

	// And the next success clears the recorded failure.
	m.store.noteSuccess(ctx, tenantID, created.ID)
	recovered, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if recovered.LastError != "" {
		t.Fatalf("a success left the old failure in place: %q", recovered.LastError)
	}
	if recovered.LastPingAt == nil {
		t.Fatal("a success did not record when it happened")
	}
	if recovered.Status != StatusActive {
		t.Fatalf("connector is %s after a success, want %s", recovered.Status, StatusActive)
	}
}

// Switching a connector off is the administrator's decision and still works.
// The fix above removes the machine's ability to do it, not the operator's.
func TestAnAdministratorCanStillSwitchAConnectorOff(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "administrator-switch-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderWebhook, Name: "Paused subscriber", TargetURL: "https://paused.example.mn/hook",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := m.Update(ctx, tenantID, created.ID, SaveRequest{
		Name: "Paused subscriber", TargetURL: "https://paused.example.mn/hook", Status: StatusInactive,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	targets, err := m.store.dispatchTargets(ctx, tenantID)
	if err != nil {
		t.Fatalf("dispatchTargets: %v", err)
	}
	for _, target := range targets {
		if target.id == created.ID {
			t.Fatal("a connector the administrator switched off is still being sent events")
		}
	}
}
