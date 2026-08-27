package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newUser creates somebody for the OAuth state row to point at. The state
// records who began a connect attempt, and a foreign key means that has to be
// a real person.
func newUser(t *testing.T, pool *pgxpool.Pool, tenantID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO registry.users (id, email, password_hash, name, is_admin) VALUES ($1, $2, '', $3, TRUE)`,
		id, "itest-"+id[:8]+"@example.mn", "Integration test administrator"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO workspace.memberships (tenant_id, user_id) VALUES ($1, $2)`, tenantID, id); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM registry.users WHERE id = $1`, id)
	})
	return id
}

// withTokenEndpoint points a provider's token exchange at a test server. The
// endpoints are otherwise the real ones, which is right everywhere except here.
func withTokenEndpoint(t *testing.T, provider Provider, url string) {
	t.Helper()
	original := specs[provider]
	patched := original
	patched.TokenURL = url
	specs[provider] = patched
	t.Cleanup(func() { specs[provider] = original })
}

// tokenEndpoint is a provider's /token: it counts what it is asked and answers
// with a fresh access token each time.
//
// It answers slowly on purpose. A renewal that returns instantly closes the
// window between reading a stale token and storing its replacement, which is
// the window the lock exists to cover — so an unlocked implementation would
// pass by luck. A real token endpoint is a round trip over the internet.
func tokenEndpoint(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-token-%d", n),
			"refresh_token": "refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// A connector is connected once and its access token is renewed every hour
// after that. Those are different events, and connected_at answers the first —
// so renewal must not touch it. Sharing one statement meant "connected since"
// crept forward all day and stopped meaning anything.
func TestRenewingATokenDoesNotRestampWhenTheAccountWasConnected(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "grant-vs-refresh-test-key")
	resetKeyForTest()

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderGoogleDrive, Name: "Archive",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := m.store.saveGrant(ctx, tenantID, created.ID, tokenBundle{
		AccessToken: "first", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour),
	}, "someone@example.mn"); err != nil {
		t.Fatalf("saveGrant: %v", err)
	}

	connected, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if connected.ConnectedAt == nil {
		t.Fatal("connecting an account did not record when")
	}
	if connected.AccountLabel != "someone@example.mn" {
		t.Fatalf("account label is %q", connected.AccountLabel)
	}

	if err := m.store.refreshTokens(ctx, tenantID, created.ID, tokenBundle{
		AccessToken: "second", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("refreshTokens: %v", err)
	}

	after, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !after.ConnectedAt.Equal(*connected.ConnectedAt) {
		t.Fatalf("renewing the token moved connected_at from %s to %s",
			connected.ConnectedAt, after.ConnectedAt)
	}
	if after.AccountLabel != "someone@example.mn" {
		t.Fatalf("renewing the token changed the account label to %q", after.AccountLabel)
	}
	tok, err := m.store.tokens(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("tokens: %v", err)
	}
	if tok.AccessToken != "second" {
		t.Fatalf("the renewed token was not stored: %q", tok.AccessToken)
	}
}

// Two exports starting together both found the same expired token. Both
// refreshed it, which is two round trips for one renewal and a race over the
// row — and with a provider that rotates refresh tokens, the loser's stored
// refresh token is one the provider has already retired.
func TestConcurrentRefreshesCollapseIntoOne(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "refresh-lock-test-key")
	t.Setenv(allowPrivateEnv, "true") // the fake token endpoint is on loopback
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	resetKeyForTest()

	var calls atomic.Int32
	server := tokenEndpoint(t, &calls)
	withTokenEndpoint(t, ProviderGoogleDrive, server.URL)

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderGoogleDrive, Name: "Archive",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Already expired, so every caller wants a new one.
	if err := m.store.saveGrant(ctx, tenantID, created.ID, tokenBundle{
		AccessToken: "stale", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Hour),
	}, ""); err != nil {
		t.Fatalf("saveGrant: %v", err)
	}

	conn, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	spec, err := SpecFor(ProviderGoogleDrive)
	if err != nil {
		t.Fatalf("spec: %v", err)
	}

	// Released together, so every caller reads the stored token before any of
	// them has had time to replace it — which is the situation two exports
	// starting at once actually produce.
	const callers = 8
	tokens := make([]string, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	for i := range callers {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			tokens[i], errs[i] = m.accessToken(ctx, tenantID, conn, spec)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if tokens[i] != tokens[0] {
			t.Fatalf("callers disagree about the token: %q and %q", tokens[0], tokens[i])
		}
		if tokens[i] == "stale" {
			t.Fatal("a caller was handed the expired token")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d callers caused %d refreshes, want 1", callers, got)
	}
}

// A token can stop working before it was due to: the provider returned no
// expires_in, or the grant was revoked and re-issued. Nothing in the flow ever
// asked whether the token was the problem, so the connector stayed broken —
// saying Connected — until somebody reconnected it by hand.
func TestARejectedTokenIsRefreshedOnceAndOnlyForARejection(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "rejection-test-key")
	t.Setenv(allowPrivateEnv, "true")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	resetKeyForTest()

	var calls atomic.Int32
	server := tokenEndpoint(t, &calls)
	withTokenEndpoint(t, ProviderGoogleDrive, server.URL)

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderGoogleDrive, Name: "Archive",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Not expired as far as this code knows — which is exactly the case the
	// expiry check cannot catch.
	if err := m.store.saveGrant(ctx, tenantID, created.ID, tokenBundle{
		AccessToken: "in-use", RefreshToken: "refresh",
	}, ""); err != nil {
		t.Fatalf("saveGrant: %v", err)
	}
	conn, err := m.Get(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	spec, _ := SpecFor(ProviderGoogleDrive)

	// Anything that is not the provider refusing the token is not a reason to
	// refresh: a missing folder or an outage would otherwise spend a renewal
	// and a retry on a request that fails identically the second time.
	notRejections := []error{
		nil,
		fmt.Errorf("Google Drive unreachable: connection reset"),
		&providerError{Provider: "Google Drive", Status: http.StatusNotFound, Body: "no such folder"},
		&providerError{Provider: "Google Drive", Status: http.StatusInternalServerError, Body: "backend error"},
	}
	for _, err := range notRejections {
		if _, retry := m.tokenAfterRejection(ctx, tenantID, conn, spec, "in-use", err); retry {
			t.Fatalf("a retry was attempted for %v", err)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("the token was refreshed %d times without the provider rejecting it", got)
	}

	// A 401 is the provider saying the token is no longer good.
	fresh, retry := m.tokenAfterRejection(ctx, tenantID, conn, spec, "in-use",
		&providerError{Provider: "Google Drive", Status: http.StatusUnauthorized, Body: "invalid credentials"})
	if !retry {
		t.Fatal("a rejected token was not refreshed")
	}
	if fresh == "in-use" || fresh == "" {
		t.Fatalf("the retry would reuse the rejected token (%q)", fresh)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("the rejection caused %d refreshes, want 1", got)
	}

	// The renewed token is what is stored, so the next call starts from it.
	stored, err := m.store.tokens(ctx, tenantID, created.ID)
	if err != nil {
		t.Fatalf("tokens: %v", err)
	}
	if stored.AccessToken != fresh {
		t.Fatalf("stored token is %q, the caller was given %q", stored.AccessToken, fresh)
	}
}

// The callback arrives from Google with no session of its own, so who did this
// is known only from the state row. Binding an outside account to a tenant is
// precisely what an audit log is for, and it was the one integration action
// that left no record.
func TestCompleteConnectNamesTheAdministratorWhoBeganIt(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "connect-audit-test-key")
	t.Setenv(allowPrivateEnv, "true")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	resetKeyForTest()

	var calls atomic.Int32
	server := tokenEndpoint(t, &calls)
	withTokenEndpoint(t, ProviderGoogleDrive, server.URL)

	pool := testPool(t)
	m := NewManager(pool)
	ctx := context.Background()
	tenantID := newTenant(t, pool)
	userID := newUser(t, pool, tenantID)

	created, err := m.Create(ctx, tenantID, SaveRequest{
		Provider: ProviderGoogleDrive, Name: "Archive",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	authURL, err := m.BeginConnect(ctx, tenantID, userID, created.ID)
	if err != nil {
		t.Fatalf("BeginConnect: %v", err)
	}
	state := stateFromAuthURL(t, authURL)

	res, err := m.CompleteConnect(ctx, state, "the-authorization-code")
	if err != nil {
		t.Fatalf("CompleteConnect: %v", err)
	}
	if res.UserID != userID {
		t.Fatalf("the grant is attributed to %q, want the administrator who began it (%q)",
			res.UserID, userID)
	}
	if res.TenantID != tenantID {
		t.Fatalf("the grant is attributed to tenant %q, want %q", res.TenantID, tenantID)
	}
	if res.Connector == nil || !res.Connector.Connected {
		t.Fatal("the connector does not report itself connected afterwards")
	}

	// The state is spent. Replaying a callback is how a grant gets bound to the
	// wrong account, so the second attempt must find nothing to redeem.
	if _, err := m.CompleteConnect(ctx, state, "the-authorization-code"); err == nil {
		t.Fatal("the same authorization state was redeemed twice")
	}
}

func stateFromAuthURL(t *testing.T, authURL string) string {
	t.Helper()
	_, query, found := strings.Cut(authURL, "?")
	if !found {
		t.Fatalf("no query in %q", authURL)
	}
	for _, pair := range strings.Split(query, "&") {
		if key, value, ok := strings.Cut(pair, "="); ok && key == "state" {
			return value
		}
	}
	t.Fatalf("no state in %q", authURL)
	return ""
}

// A truncated string is written to the database, and Postgres refuses invalid
// UTF-8. Cutting on a byte boundary therefore did not shorten a Cyrillic error
// message — it made the UPDATE that records the failure fail, silently, since
// noteError ignores its own errors. The failure vanished instead of appearing.
func TestTruncateNeverSplitsACharacter(t *testing.T) {
	const cyrillic = "Гэрээний файлыг байршуулах үед алдаа гарлаа"
	for n := range len(cyrillic) + 4 {
		got := truncate(cyrillic, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%d) produced invalid UTF-8: %q", n, []byte(got))
		}
		if len(got) > n {
			t.Fatalf("truncate(%d) returned %d bytes", n, len(got))
		}
		if !strings.HasPrefix(cyrillic, got) {
			t.Fatalf("truncate(%d) = %q, which is not a prefix", n, got)
		}
	}

	if got := truncate("ascii", 100); got != "ascii" {
		t.Fatalf("a short string came back as %q", got)
	}
	if got := truncate("Гэрээ", 0); got != "" {
		t.Fatalf("truncate to nothing returned %q", got)
	}
}
