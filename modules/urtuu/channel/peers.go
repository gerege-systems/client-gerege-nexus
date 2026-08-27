/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Establishing a link, and ending one.
 *
 * The flow is report_grants' (§2.3, docs/URTUU_PROPOSAL.md §5) with one extra
 * step, because unlike two organisations on the same deployment these two
 * parties have never met: the parent issues a single-use invitation, the child
 * redeems it and hands over its public key, and the parent confirms. Nothing is
 * exchanged until both sides have acted, and either side can end it afterwards.
 *
 * There is no DELETE. A link is revoked, never removed — "who were we connected
 * to, and when" is a question asked long after the answer would have been
 * deleted.
 */

package channel

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	// inviteWindow is how long an invitation may be redeemed in. A day is long
	// enough to pass a code to somebody in another organisation by telephone
	// and short enough that a code left in a chat log stops working.
	inviteWindow = 24 * time.Hour

	// maxPeerBody bounds an administrative request body.
	maxPeerBody = 8 << 10

	// wellKnownTimeout bounds the one call a child makes before any link
	// exists. A parent that is merely slow must not hold an administrator's
	// browser open.
	wellKnownTimeout = 10 * time.Second
)

// Peer is one link as the screens see it. The token never appears here; the
// invitation code appears exactly once, in the response that creates it.
type Peer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is what *we* are on this link — the direction, from here.
	Role      string `json:"role"`
	BaseURL   string `json:"base_url,omitempty"`
	Status    string `json:"status"`
	PublicKey string `json:"peer_public_key,omitempty"`
	// InviteExpiresAt is set while an invitation is outstanding, so the screen
	// can say a code has run out rather than only that nobody used it.
	InviteExpiresAt  *time.Time `json:"invite_expires_at,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	ClockSkewSeconds int        `json:"clock_skew_seconds"`
	Undelivered      int        `json:"undelivered"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// handleListPeers is the Settings → Өртөө screen.
func (s *Service) handleListPeers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}
	peers, err := s.listPeers(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the links")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{
		"peers":           peers,
		"enabled":         s.Enabled(),
		"installation_id": s.installationID,
		"public_key":      s.PublicKey(),
	})
}

// handleInvite is the parent opening a link. It creates the row before anybody
// has redeemed anything, which is what makes the invitation single-use: the
// code is stored hashed against that one row, and redeeming it clears the hash.
func (s *Service) handleInvite(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.requireEnabled(w, r)
	if !ok {
		return
	}

	var request struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &request) {
		return
	}

	code, err := inviteCode()
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not create an invitation")
		return
	}

	var id string
	err = s.db.QueryRow(nexus.WithWorkspaceID(r.Context(), tenantID), `
		INSERT INTO urtuu_peers
		    (tenant_id, name, role, status, invite_code_hash, invite_expires_at, installation_id, created_by)
		VALUES ($1, $2, 'parent', 'pending', $3, NOW() + $4::interval, $5, NULLIF($6, '')::uuid)
		RETURNING id`,
		tenantID, strings.TrimSpace(request.Name), inviteHash(code),
		fmt.Sprintf("%d seconds", int(inviteWindow.Seconds())), s.installationID, actorOf(r)).Scan(&id)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not create an invitation")
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.peer_invited", "urtuu_peer",
		map[string]any{"peer_id": id})
	// The only time the code is ever legible. It is not stored, so a lost one
	// is replaced by revoking the link and inviting again.
	nexus.JSON(w, http.StatusCreated, map[string]any{
		"id": id, "invite_code": code, "expires_in_hours": int(inviteWindow.Hours()),
	})
}

// handleJoin is the child end of the handshake, and the only place this
// platform reaches out to a peer on an administrator's behalf.
//
// Three things happen and all three have to succeed: the parent's key is
// fetched from its well-known document, the local row is created, and the
// invitation is redeemed at the parent. The row is written first so that the
// token — which is derived from its id — exists to be presented; a redemption
// that then fails leaves a pending row the administrator can see and revoke,
// which is a better end state than a silent nothing.
func (s *Service) handleJoin(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.requireEnabled(w, r)
	if !ok {
		return
	}

	var request struct {
		InviteCode string `json:"invite_code"`
		BaseURL    string `json:"base_url"`
		Name       string `json:"name"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.InviteCode) == "" {
		nexus.Error(w, http.StatusBadRequest, "an invitation code is required")
		return
	}
	baseURL, err := normalizeBaseURL(request.BaseURL)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	parent, err := s.fetchWellKnown(r.Context(), baseURL)
	if err != nil {
		nexus.Error(w, http.StatusBadGateway, "could not read the parent's Өртөө identity: "+err.Error())
		return
	}
	if parent.InstallationID == s.installationID {
		// Not pedantry: a link to itself would make every task its own
		// ancestor and the cycle guard would have nothing to catch.
		nexus.Error(w, http.StatusBadRequest, "that address is this installation")
		return
	}

	ctx := nexus.WithWorkspaceID(r.Context(), tenantID)
	var id string
	err = s.db.QueryRow(ctx, `
		INSERT INTO urtuu_peers
		    (tenant_id, name, role, base_url, peer_public_key, status, installation_id, created_by)
		VALUES ($1, $2, 'child', $3, $4, 'pending', $5, NULLIF($6, '')::uuid)
		RETURNING id`,
		tenantID, strings.TrimSpace(request.Name), baseURL, parent.PublicKey,
		s.installationID, actorOf(r)).Scan(&id)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not record the link")
		return
	}

	if err := s.redeemAtParent(r.Context(), baseURL, request.InviteCode, s.peerToken(id)); err != nil {
		// Left as pending with the reason on it rather than deleted: an
		// administrator who mistyped a code needs to see what happened, and the
		// row is the only place that can say so.
		_, _ = s.db.Exec(ctx,
			`UPDATE urtuu_peers SET last_error = $2, updated_at = NOW() WHERE id = $1`, id, err.Error())
		nexus.Error(w, http.StatusBadGateway, "the parent did not accept the invitation: "+err.Error())
		return
	}

	// Active from here: this side has everything it needs. The parent's own row
	// stays pending until its administrator confirms, so nothing is exchanged
	// yet — pull and push answer 403 until then.
	if _, err := s.db.Exec(ctx,
		`UPDATE urtuu_peers SET status = 'active', last_error = '', updated_at = NOW() WHERE id = $1`,
		id); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not activate the link")
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.peer_joined", "urtuu_peer",
		map[string]any{"peer_id": id, "base_url": baseURL, "parent_installation_id": parent.InstallationID})
	nexus.JSON(w, http.StatusCreated, map[string]any{"id": id, "parent_installation_id": parent.InstallationID})
}

// HandleRedeem is the parent's side of the redemption. It is reached without a
// session — the invitation code is the credential, and the caller is another
// installation rather than a person.
func (s *Service) HandleRedeem(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled() {
		nexus.Error(w, http.StatusNotFound, "this installation does not run Өртөө")
		return
	}

	var request struct {
		InviteCode     string `json:"invite_code"`
		PublicKey      string `json:"public_key"`
		InstallationID string `json:"installation_id"`
		Name           string `json:"name"`
		Token          string `json:"token"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.PublicKey) == "" || strings.TrimSpace(request.Token) == "" {
		nexus.Error(w, http.StatusBadRequest, "a public key and a token are required")
		return
	}

	// The platform path: an invitation is not held by anybody yet, so there is
	// no tenant to resolve it in. The code's hash is the whole lookup, and it
	// is what carries the tenant back.
	ctx := nexus.WithoutWorkspace(r.Context())
	var id, tenantID string
	err := s.db.QueryRow(ctx, `
		UPDATE urtuu_peers
		   SET peer_public_key = $2,
		       token_hash = $3,
		       name = CASE WHEN name = '' THEN $4 ELSE name END,
		       -- Cleared, not kept: the invitation was the introduction and the
		       -- token is the credential from here. A code that still worked
		       -- would be a second way in that nobody is watching.
		       invite_code_hash = NULL,
		       invite_expires_at = NULL,
		       updated_at = NOW()
		 WHERE invite_code_hash = $1
		   AND invite_expires_at > NOW()
		   AND status = 'pending'
		   AND revoked_at IS NULL
		 RETURNING id, tenant_id`,
		inviteHash(request.InviteCode), strings.TrimSpace(request.PublicKey),
		tokenHash(request.Token), strings.TrimSpace(request.Name)).Scan(&id, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		// One answer for expired, spent, revoked and never-existed. Telling
		// them apart would turn this endpoint into an oracle for guessing
		// codes.
		nexus.Error(w, http.StatusForbidden, "that invitation is not open")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not redeem the invitation")
		return
	}

	nexus.Audit(ctx, tenantID, "", "urtuu.peer_redeemed", "urtuu_peer",
		map[string]any{"peer_id": id, "child_installation_id": request.InstallationID})
	nexus.JSON(w, http.StatusOK, wellKnown{
		InstallationID: s.installationID,
		PublicKey:      s.PublicKey(),
		Protocol:       Protocol,
	})
}

// handleConfirm is the parent agreeing, which is what opens the channel.
func (s *Service) handleConfirm(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := s.peerParty(w, r)
	if !ok {
		return
	}

	tag, err := s.db.Exec(nexus.WithWorkspaceID(r.Context(), tenantID), `
		UPDATE urtuu_peers SET status = 'active', updated_at = NOW()
		 WHERE id = $1 AND status = 'pending' AND revoked_at IS NULL
		   -- Only a link somebody has actually redeemed: confirming one that
		   -- still has an outstanding invitation would activate a peer whose
		   -- key this installation has never seen.
		   AND peer_public_key <> '' AND token_hash <> ''`, id)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not confirm the link")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusNotFound, "no link is waiting to be confirmed")
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.peer_confirmed", "urtuu_peer",
		map[string]any{"peer_id": id})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id, "status": "active"})
}

// handleRevoke ends a link from either side. The row stays.
func (s *Service) handleRevoke(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := s.peerParty(w, r)
	if !ok {
		return
	}

	tag, err := s.db.Exec(nexus.WithWorkspaceID(r.Context(), tenantID), `
		UPDATE urtuu_peers
		   SET status = 'revoked', revoked_at = NOW(),
		       -- The credential goes with the link. Revoked is checked on every
		       -- exchange anyway; clearing the hash means a token that leaked
		       -- before the revocation cannot match anything even if a later
		       -- change to that check gets it wrong.
		       token_hash = '', invite_code_hash = NULL, updated_at = NOW()
		 WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not revoke the link")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusNotFound, "no such link")
		return
	}

	nexus.Audit(r.Context(), tenantID, actorOf(r), "urtuu.peer_revoked", "urtuu_peer",
		map[string]any{"peer_id": id})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id, "status": "revoked"})
}

// fetchWellKnown reads a prospective parent's identity.
func (s *Service) fetchWellKnown(ctx context.Context, baseURL string) (wellKnown, error) {
	ctx, cancel := context.WithTimeout(ctx, wellKnownTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/.well-known/urtuu.json", nil)
	if err != nil {
		return wellKnown{}, err
	}
	req.Header.Set("Accept", "application/json")

	res, err := s.client.Do(req)
	if err != nil {
		return wellKnown{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return wellKnown{}, fmt.Errorf("answered %s", res.Status)
	}

	var document wellKnown
	if err := json.NewDecoder(io.LimitReader(res.Body, maxPeerBody)).Decode(&document); err != nil {
		return wellKnown{}, err
	}
	if _, err := decodePublicKey(document.PublicKey); err != nil {
		return wellKnown{}, err
	}
	if document.Protocol != Protocol {
		return wellKnown{}, fmt.Errorf("speaks %q and this installation speaks %q", document.Protocol, Protocol)
	}
	return document, nil
}

// redeemAtParent hands the parent this link's public key and token.
func (s *Service) redeemAtParent(ctx context.Context, baseURL, code, token string) error {
	ctx, cancel := context.WithTimeout(ctx, wellKnownTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]string{
		"invite_code": code, "public_key": s.PublicKey(),
		"installation_id": s.installationID, "token": token,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/urtuu/peers/redeem", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %s", res.Status)
	}
	return nil
}

// inviteCode is what one administrator reads out to another: twenty base32
// characters in five groups, the shape device enrolment already uses.
func inviteCode() (string, error) {
	value := make([]byte, 15)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	raw := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)
	return raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:], nil
}

// requireEnabled resolves the caller's organisation and refuses when this
// installation has no Өртөө identity to act with.
func (s *Service) requireEnabled(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return "", false
	}
	if !s.Enabled() {
		nexus.Error(w, http.StatusServiceUnavailable,
			"Өртөө is not configured on this installation: "+signingKeyEnv+" is unset")
		return "", false
	}
	return tenantID, true
}

// peerParty resolves the caller's organisation and the link id together.
func (s *Service) peerParty(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return "", "", false
	}
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid link id")
		return "", "", false
	}
	return tenantID, id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPeerBody)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

func actorOf(r *http.Request) string {
	if claims, err := nexus.UserFromContext(r.Context()); err == nil {
		return claims.UserID
	}
	return ""
}
