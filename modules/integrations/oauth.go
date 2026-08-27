package integrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// oauthStateTTL bounds how long a half-finished connect attempt stays valid.
// Long enough for someone to read a consent screen and pick an account; short
// enough that an abandoned attempt is not a standing invitation.
const oauthStateTTL = 15 * time.Minute

// RedirectURI is the one address every provider returns to.
//
// It is derived from PUBLIC_ORIGIN rather than from the incoming request:
// an OAuth redirect URI has to match what is registered with the provider
// exactly, and taking it from the request Host lets a caller with a forged
// Host header point the callback — and the authorization code with it —
// somewhere else.
func RedirectURI() string {
	origin := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_ORIGIN")), "/")
	if origin == "" {
		origin = "http://localhost:8080"
	}
	return origin + "/api/v1/integrations/oauth/callback"
}

// BeginConnect returns the provider URL to send the administrator to.
func (m *Manager) BeginConnect(ctx context.Context, tenantID, userID, id string) (string, error) {
	conn, err := m.store.get(ctx, tenantID, id)
	if err != nil {
		return "", err
	}
	spec, err := SpecFor(conn.Provider)
	if err != nil {
		return "", err
	}
	if !spec.OAuth {
		return "", fmt.Errorf("%s connectors are configured with a URL, not an account", conn.Provider)
	}
	clientID, _, err := spec.Credentials()
	if err != nil {
		return "", err
	}
	if !EncryptionConfigured() {
		return "", ErrNoEncryptionKey
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("integration: generate oauth state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)
	redirect := RedirectURI()

	if _, err := m.store.db.Exec(ctx,
		`INSERT INTO workspace.integration_oauth_states (state, tenant_id, integration_id, user_id, redirect_uri, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		state, tenantID, conn.ID, userID, redirect, time.Now().Add(oauthStateTTL)); err != nil {
		return "", err
	}

	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"state":         {state},
		"scope":         {strings.Join(spec.Scopes, " ")},
	}
	if conn.Provider == ProviderGoogleDrive || conn.Provider == ProviderGoogleMeet {
		// Google only issues a refresh token on the first consent unless it is
		// asked to prompt again. Without one, the connector works until the
		// access token expires an hour later and then quietly stops — so the
		// consent screen is forced every time rather than only when it happens
		// to be the first.
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
		q.Set("include_granted_scopes", "true")
	}
	if conn.Provider == ProviderDropbox {
		// Dropbox's equivalent: short-lived access tokens plus a refresh token.
		q.Set("token_access_type", "offline")
	}

	return spec.AuthURL + "?" + q.Encode(), nil
}

// ConnectResult is what a completed grant leaves the caller to act on.
//
// The callback arrives from the provider with no session of its own, so
// everything the platform knows about who this is comes out of the redeemed
// state row: the tenant it belongs to, and the administrator who started it.
// UserID is the reason the row records one — binding an outside account to a
// tenant is exactly the kind of act the audit log exists for, and without this
// it was the one integration action that went unrecorded.
type ConnectResult struct {
	TenantID  string
	UserID    string
	Connector *Connector
}

// CompleteConnect redeems an authorization code and stores the grant.
func (m *Manager) CompleteConnect(ctx context.Context, state, code string) (*ConnectResult, error) {
	if state == "" || code == "" {
		return nil, errors.New("the provider did not return a state and code")
	}

	// Redeem the state: the DELETE ... RETURNING makes it single-use even if
	// two callbacks arrive at once, which is what makes a leaked state useless
	// after the first attempt.
	var tenantID, integrationID, userID string
	err := m.store.db.QueryRow(ctx,
		`DELETE FROM workspace.integration_oauth_states
		  WHERE state = $1 AND expires_at > NOW()
		 RETURNING tenant_id::text, integration_id::text, user_id::text`, state).
		Scan(&tenantID, &integrationID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("this authorization link has expired or was already used")
	}
	if err != nil {
		return nil, err
	}

	conn, err := m.store.get(ctx, tenantID, integrationID)
	if err != nil {
		return nil, err
	}
	spec, err := SpecFor(conn.Provider)
	if err != nil {
		return nil, err
	}

	tok, err := m.exchangeCode(ctx, spec, code)
	if err != nil {
		m.store.noteError(ctx, tenantID, conn.ID, err.Error())
		return nil, err
	}

	label := m.accountLabel(ctx, conn.Provider, tok.AccessToken)
	if err := m.store.saveGrant(ctx, tenantID, conn.ID, tok, label); err != nil {
		return nil, err
	}

	conn, err = m.store.get(ctx, tenantID, integrationID)
	if err != nil {
		return nil, err
	}
	return &ConnectResult{TenantID: tenantID, UserID: userID, Connector: conn}, nil
}

// exchangeCode swaps an authorization code for tokens.
func (m *Manager) exchangeCode(ctx context.Context, spec Spec, code string) (tokenBundle, error) {
	clientID, clientSecret, err := spec.Credentials()
	if err != nil {
		return tokenBundle{}, err
	}
	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {RedirectURI()},
		"grant_type":    {"authorization_code"},
	}
	return m.postToken(ctx, spec, form)
}

// refresh exchanges a refresh token for a new access token.
func (m *Manager) refresh(ctx context.Context, spec Spec, refreshToken string) (tokenBundle, error) {
	clientID, clientSecret, err := spec.Credentials()
	if err != nil {
		return tokenBundle{}, err
	}
	form := url.Values{
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"refresh_token"},
	}
	tok, err := m.postToken(ctx, spec, form)
	if err != nil {
		return tokenBundle{}, err
	}
	// A refresh response usually omits the refresh token, which means "keep
	// using the one you have" — not "you no longer have one".
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

func (m *Manager) postToken(ctx context.Context, spec Spec, form url.Values) (tokenBundle, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, spec.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return tokenBundle{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return tokenBundle{}, fmt.Errorf("%s token endpoint unreachable: %w", spec.Provider, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenBundle{}, err
	}
	if resp.StatusCode >= 300 {
		// The body carries the provider's own reason — invalid_grant,
		// redirect_uri_mismatch — which is the only thing that tells an
		// operator which of the two ends is misconfigured.
		return tokenBundle{}, fmt.Errorf("%s refused the token request (%d): %s",
			spec.Provider, resp.StatusCode, strings.TrimSpace(truncate(string(body), 300)))
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return tokenBundle{}, fmt.Errorf("%s returned an unreadable token response: %w", spec.Provider, err)
	}
	if payload.AccessToken == "" {
		return tokenBundle{}, fmt.Errorf("%s returned no access token", spec.Provider)
	}

	tok := tokenBundle{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
		Scope:        payload.Scope,
	}
	if payload.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// accessToken returns a usable access token, refreshing and re-storing it when
// the stored one has expired.
//
// Refreshing here rather than on a schedule means a connector that is never
// used never spends a request, and one that is used always has a live token.
func (m *Manager) accessToken(ctx context.Context, tenantID string, conn *Connector, spec Spec) (string, error) {
	tok, err := m.store.tokens(ctx, tenantID, conn.ID)
	if err != nil {
		return "", err
	}
	if !tok.expired() {
		return tok.AccessToken, nil
	}
	return m.refreshAccess(ctx, tenantID, conn, spec, tok.AccessToken)
}

// refreshAccess exchanges the refresh token for a new access token, once.
//
// The lock is per connector, and the stored bundle is re-read under it. Two
// exports starting together both saw the same expired token and both refreshed
// it, which is two round trips for one renewal and a last-write-wins race over
// the row — harmless with a provider that reuses refresh tokens and a lost
// grant with one that rotates them. Whoever gets the lock second finds the
// token already replaced and uses it.
//
// stale is the token that turned out to be unusable. Comparing against it is
// what makes this safe to call both for an expiry the caller predicted and for
// a rejection the provider announced.
func (m *Manager) refreshAccess(ctx context.Context, tenantID string, conn *Connector,
	spec Spec, stale string) (string, error) {

	lock, _ := m.refreshLocks.LoadOrStore(conn.ID, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	tok, err := m.store.tokens(ctx, tenantID, conn.ID)
	if err != nil {
		return "", err
	}
	if tok.AccessToken != stale && tok.AccessToken != "" {
		// Someone else refreshed while this call waited for the lock.
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		return "", fmt.Errorf(
			"the %s grant has expired and carries no refresh token — reconnect the account", conn.Provider)
	}

	refreshed, err := m.refresh(ctx, spec, tok.RefreshToken)
	if err != nil {
		return "", err
	}
	if err := m.store.refreshTokens(ctx, tenantID, conn.ID, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// tokenAfterRejection answers what to do when a provider refuses the token.
//
// A stored token can stop working before it is due to: the provider returned no
// expires_in and this code therefore never treated it as expiring, an
// administrator revoked and re-granted, or the clock disagreed. Without this
// the connector stayed broken until somebody reconnected it by hand, because
// nothing in the flow ever asked whether the token was the problem.
//
// It refreshes at most once — refresh returning a token the provider also
// rejects is a grant that is genuinely gone, and retrying that is a loop.
func (m *Manager) tokenAfterRejection(ctx context.Context, tenantID string, conn *Connector,
	spec Spec, used string, err error) (string, bool) {

	var refusal *providerError
	if !errors.As(err, &refusal) || refusal.Status != http.StatusUnauthorized {
		return "", false
	}
	fresh, refreshErr := m.refreshAccess(ctx, tenantID, conn, spec, used)
	if refreshErr != nil || fresh == used {
		return "", false
	}
	return fresh, true
}

// accountLabel asks the provider whose account was just connected. It is
// best-effort: a connector with an unnamed account still works, and failing the
// whole connect over a cosmetic field would be the wrong trade.
func (m *Manager) accountLabel(ctx context.Context, provider Provider, accessToken string) string {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var endpoint, method string
	switch provider {
	case ProviderGoogleDrive, ProviderGoogleMeet:
		endpoint, method = "https://openidconnect.googleapis.com/v1/userinfo", http.MethodGet
	case ProviderDropbox:
		endpoint, method = "https://api.dropboxapi.com/2/users/get_current_account", http.MethodPost
	default:
		return ""
	}

	req, err := http.NewRequestWithContext(reqCtx, method, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= 300 {
		return ""
	}

	// Google's "name" is a string and Dropbox's is an object, so no single
	// struct decodes both. Two shapes, each tried, keeps them readable without
	// branching on the provider a second time.
	var google struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	_ = json.Unmarshal(body, &google)
	var dropbox struct {
		Email string `json:"email"`
		Name  struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
	}
	_ = json.Unmarshal(body, &dropbox)

	switch {
	case google.Email != "":
		return truncate(google.Email, 255)
	case dropbox.Email != "":
		return truncate(dropbox.Email, 255)
	case dropbox.Name.DisplayName != "":
		return truncate(dropbox.Name.DisplayName, 255)
	case google.Name != "":
		return truncate(google.Name, 255)
	}
	return ""
}

// SweepOAuthStates deletes connect attempts nobody finished.
func (m *Manager) SweepOAuthStates(ctx context.Context) (int64, error) {
	tag, err := m.store.db.Exec(ctx,
		`DELETE FROM workspace.integration_oauth_states WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// truncate cuts a string to at most n bytes without splitting a character.
//
// s[:n] on its own produces invalid UTF-8 whenever the cut lands inside a
// multi-byte rune, and these strings are written to the database: Postgres
// refuses an invalid byte sequence outright. A provider error message with a
// Cyrillic word near the limit therefore failed the UPDATE that records the
// failure — and noteError deliberately ignores its own errors, so the failure
// disappeared instead of being reported. The same cut names the account a
// document is about to be filed in.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	// Back up to the start of the rune the cut landed in the middle of.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
