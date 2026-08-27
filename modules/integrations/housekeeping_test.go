package integrations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Two tables here only ever grow. integration_oauth_states gains a row every
// time somebody presses Connect and loses one only when they come back from the
// consent screen, so every abandoned attempt stayed forever — SweepOAuthStates
// was written for exactly this and nothing ever called it. The delivery log
// gains a row for every event, export and meeting.
func TestHousekeepingRemovesOnlyWhatIsFinishedWith(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "housekeeping-test-key")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)
	userID := newUser(t, pool, tenantID)

	conn, err := m.Create(ctx, tenantID, SaveRequest{Provider: ProviderGoogleDrive, Name: "Archive"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	abandoned, live := uuid.NewString(), uuid.NewString()
	for state, expiry := range map[string]time.Time{
		abandoned: time.Now().Add(-time.Hour),
		live:      time.Now().Add(10 * time.Minute),
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO workspace.integration_oauth_states
			     (state, tenant_id, integration_id, user_id, redirect_uri, expires_at)
			 VALUES ($1, $2, $3, $4, '', $5)`,
			state, tenantID, conn.ID, userID, expiry); err != nil {
			t.Fatalf("insert state: %v", err)
		}
	}

	// One delivery past the retention horizon and one inside it.
	m.store.recordDelivery(ctx, tenantID, conn.ID, "file", "old", "OK", "", "", "")
	m.store.recordDelivery(ctx, tenantID, conn.ID, "file", "recent", "OK", "", "", "")
	if _, err := pool.Exec(ctx,
		`UPDATE workspace.integration_deliveries SET created_at = $2 WHERE tenant_id = $1 AND reference = 'old'`,
		tenantID, time.Now().Add(-deliveryRetention-24*time.Hour)); err != nil {
		t.Fatalf("age the delivery: %v", err)
	}

	m.sweepOnce(ctx)

	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM workspace.integration_oauth_states WHERE state = $1`, abandoned).
		Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Fatal("an abandoned connect attempt survived the sweep")
	}

	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM workspace.integration_oauth_states WHERE state = $1`, live).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatal("the sweep removed an attempt somebody is still in the middle of")
	}

	deliveries, err := m.Deliveries(ctx, tenantID, 100)
	if err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Reference != "recent" {
		t.Fatalf("the delivery log holds %d rows after the sweep, want only the recent one", len(deliveries))
	}
}

// The name is how an operator picks a destination, so two connectors called
// "Archive" is a way to file a signed document into the wrong account. The
// constraint said so; the API answered with the constraint's own name in a 400.
func TestADuplicateNameIsAnAnswerNotADriverMessage(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "duplicate-name-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	req := SaveRequest{Provider: ProviderWebhook, Name: "Archive", TargetURL: "https://a.example.mn/hook"}
	if _, err := m.Create(ctx, tenantID, req); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := m.Create(ctx, tenantID, req)
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("the second create returned %v, want ErrDuplicateName", err)
	}
	// And it reads as a sentence about the name, not as the table's internals.
	for _, leak := range []string{"integrations_name_unique_per_tenant", "SQLSTATE", "23505"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("the message leaks %q: %v", leak, err)
		}
	}
}
