/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The four tables, and the handful of questions asked of them.
 */

package channel

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// peerRow is a link as the transport uses it, credential included.
type peerRow struct {
	ID        string
	TenantID  string
	Name      string
	Role      string
	BaseURL   string
	PublicKey ed25519.PublicKey
	Status    string
}

// decodePublicKey turns a stored base64 key into one that can verify.
func decodePublicKey(raw string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("the peer's public key is not base64")
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the peer's public key is %d bytes, want %d", len(decoded), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(decoded), nil
}

// listPeers reads one organisation's links, with the one number that says
// whether a link is working: how much is queued for it and has not landed.
func (s *Service) listPeers(ctx context.Context, tenantID string) ([]Peer, error) {
	rows, err := s.db.Query(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT p.id::text, p.name, p.role, p.base_url, p.status, p.peer_public_key,
		       p.invite_expires_at, p.last_seen_at, p.last_error, p.clock_skew_seconds,
		       p.revoked_at, p.created_at,
		       (SELECT count(*) FROM urtuu_deliveries d
		         WHERE d.peer_id = p.id AND d.delivered_at IS NULL)
		  FROM urtuu_peers p
		 WHERE p.tenant_id = $1
		 ORDER BY p.created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	peers := make([]Peer, 0, 16)
	for rows.Next() {
		var peer Peer
		if err := rows.Scan(&peer.ID, &peer.Name, &peer.Role, &peer.BaseURL, &peer.Status,
			&peer.PublicKey, &peer.InviteExpiresAt, &peer.LastSeenAt, &peer.LastError,
			&peer.ClockSkewSeconds, &peer.RevokedAt, &peer.CreatedAt, &peer.Undelivered); err != nil {
			return nil, err
		}
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

// peerByToken resolves the caller of an exchange endpoint.
//
// On the platform path, and it has to be: a bearer token arrives with no
// session and therefore no organisation, and the row it names is what carries
// the organisation back. Everything the handler does afterwards runs inside
// that tenant.
func (s *Service) peerByToken(ctx context.Context, token string) (peerRow, error) {
	var row peerRow
	var key string
	err := s.db.QueryRow(nexus.WithoutWorkspace(ctx), `
		SELECT id::text, tenant_id::text, name, role, base_url, peer_public_key, status
		  FROM urtuu_peers
		 -- installation_id: a token is only this installation's to honour if the
		 -- link was established with this installation's key. One database holds
		 -- one installation in the field, so this is normally a constant — but it
		 -- is the clause that makes "whose link is this" answerable at all, and
		 -- it is what a key rotation shows up in.
		 WHERE token_hash = $1 AND installation_id = $2 AND revoked_at IS NULL`,
		tokenHash(token), s.installationID).
		Scan(&row.ID, &row.TenantID, &row.Name, &row.Role, &row.BaseURL, &key, &row.Status)
	if err != nil {
		return peerRow{}, err
	}
	row.PublicKey, err = decodePublicKey(key)
	return row, err
}

// activeChildLinks lists the links this installation is the child on, across
// every organisation. The exchange loop is housekeeping — it crosses tenants
// deliberately — so it runs on the platform path and carries each row's own
// tenant into the work it does for it.
func (s *Service) activeChildLinks(ctx context.Context) ([]peerRow, error) {
	rows, err := s.db.Query(nexus.WithoutWorkspace(ctx), `
		SELECT id::text, tenant_id::text, name, role, base_url, peer_public_key, status
		  FROM urtuu_peers
		 WHERE role = 'child' AND status = 'active' AND revoked_at IS NULL AND base_url <> ''
		   -- Only links this installation can actually speak for: the bearer
		   -- token on each is derived from this installation's signing key, so a
		   -- row established under another one would be dialled with a
		   -- credential the far end has never seen.
		   AND installation_id = $1`, s.installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	links := make([]peerRow, 0, 8)
	for rows.Next() {
		var row peerRow
		var key string
		if err := rows.Scan(&row.ID, &row.TenantID, &row.Name, &row.Role, &row.BaseURL, &key, &row.Status); err != nil {
			return nil, err
		}
		// A link whose key cannot be decoded can still send but could never
		// verify an answer, which is worse than being skipped and said out
		// loud by the caller.
		publicKey, err := decodePublicKey(key)
		if err != nil {
			continue
		}
		row.PublicKey = publicKey
		links = append(links, row)
	}
	return links, rows.Err()
}

// Enqueue signs a payload and queues it for one or more links.
//
// This is the whole outbound API other packages use: the Өртөө app calls it to
// send a task, and Ө2's code announcement calls it with every child at once.
// One envelope, signed once, with a delivery row per peer — fan-out must not
// mean fan-out of signatures.
func (s *Service) Enqueue(ctx context.Context, tenantID, kind string, payload any, peerIDs ...string) (string, error) {
	if !s.Enabled() {
		return "", errors.New("urtuu: this installation has no signing key")
	}
	if len(peerIDs) == 0 {
		return "", errors.New("urtuu: an envelope with no destination")
	}

	envelope, err := s.sign(kind, payload)
	if err != nil {
		return "", err
	}

	ctx = nexus.WithWorkspaceID(ctx, tenantID)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.queue(ctx, tx, tenantID, envelope, peerIDs); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return envelope.MessageID, nil
}

// EnqueueTx is Enqueue inside somebody else's transaction.
//
// It exists for one specific correctness problem, which the Өртөө app hits on
// every fan-out: the app writes the rows that stand for work sent downward and
// queues the envelopes that actually send it, and those two facts must land
// together. With a transaction of its own here, an app transaction that rolled
// back afterwards would leave envelopes queued for work no row remembers — and
// the other installation would do it.
func (s *Service) EnqueueTx(ctx context.Context, tx pgx.Tx, tenantID, kind string, payload any, peerIDs ...string) (string, error) {
	if !s.Enabled() {
		return "", errors.New("urtuu: this installation has no signing key")
	}
	if len(peerIDs) == 0 {
		return "", errors.New("urtuu: an envelope with no destination")
	}
	envelope, err := s.sign(kind, payload)
	if err != nil {
		return "", err
	}
	if err := s.queue(ctx, tx, tenantID, envelope, peerIDs); err != nil {
		return "", err
	}
	return envelope.MessageID, nil
}

// sign builds and signs one envelope.
func (s *Service) sign(kind string, payload any) (contract.Envelope, error) {
	envelope, err := contract.New(uuid.NewString(), kind, time.Now(), payload)
	if err != nil {
		return contract.Envelope{}, err
	}
	return contract.Sign(s.signing, envelope)
}

// queue writes one signed envelope and a delivery row per live link.
func (s *Service) queue(ctx context.Context, tx pgx.Tx, tenantID string, envelope contract.Envelope, peerIDs []string) error {
	var outboxID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO urtuu_outbox (tenant_id, message_id, kind, created_at, payload, signature)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		tenantID, envelope.MessageID, envelope.Kind, envelope.CreatedAt,
		string(envelope.Payload), envelope.Signature).Scan(&outboxID); err != nil {
		return err
	}

	for _, peerID := range peerIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO urtuu_deliveries (tenant_id, outbox_id, peer_id)
			SELECT $1, $2, id FROM urtuu_peers
			 WHERE id = $3 AND tenant_id = $1 AND status = 'active' AND revoked_at IS NULL`,
			tenantID, outboxID, peerID); err != nil {
			return err
		}
	}
	return nil
}

// dueEnvelopes takes what is waiting for one link and books the next attempt in
// the same statement.
//
// Booking before sending is what makes a lost response cost a repeat rather
// than a loss: the delivery stays undelivered and comes round again on the
// backoff, and the receiver's message_id uniqueness makes the repeat free. The
// alternative — mark delivered when the send returns — turns every dropped
// response into work that silently never happened.
func (s *Service) dueEnvelopes(ctx context.Context, peer peerRow, limit int) ([]contract.Envelope, error) {
	ctx = nexus.WithWorkspaceID(ctx, peer.TenantID)
	rows, err := s.db.Query(ctx, `
		WITH due AS (
		    SELECT d.id
		      FROM urtuu_deliveries d
		     WHERE d.peer_id = $1 AND d.delivered_at IS NULL AND d.next_attempt_at <= NOW()
		     ORDER BY d.next_attempt_at
		     LIMIT $2
		     FOR UPDATE SKIP LOCKED
		)
		UPDATE urtuu_deliveries d
		   SET attempts = d.attempts + 1,
		       -- Exponential, capped. 30s, a minute, two… up to six hours,
		       -- which is the pace a link that has been down for a day should
		       -- be retried at: often enough to recover unattended, rarely
		       -- enough not to be a load generator pointed at a peer that is
		       -- already in trouble.
		       next_attempt_at = NOW() + LEAST(INTERVAL '6 hours',
		                                       INTERVAL '30 seconds' * POWER(2, LEAST(d.attempts, 10)))
		  FROM due, urtuu_outbox o
		 WHERE d.id = due.id AND o.id = d.outbox_id
		 RETURNING o.message_id, o.kind, o.created_at, o.payload, o.signature`, peer.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	envelopes := make([]contract.Envelope, 0, limit)
	for rows.Next() {
		var envelope contract.Envelope
		var payload string
		if err := rows.Scan(&envelope.MessageID, &envelope.Kind, &envelope.CreatedAt,
			&payload, &envelope.Signature); err != nil {
			return nil, err
		}
		// Read back, never re-rendered — the same rule the payload follows and
		// for the same reason. It was a timestamptz once, and the round trip
		// through it silently dropped the nanoseconds Go had signed: on Linux,
		// where the clock has them, every envelope this platform sent failed
		// verification at the far end. See migration 00067.
		envelope.Payload = []byte(payload)
		envelopes = append(envelopes, envelope)
	}
	return envelopes, rows.Err()
}

// markDelivered closes off the deliveries a peer says it has stored.
func (s *Service) markDelivered(ctx context.Context, peer peerRow, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	tag, err := s.db.Exec(nexus.WithWorkspaceID(ctx, peer.TenantID), `
		UPDATE urtuu_deliveries d
		   SET delivered_at = NOW(), last_error = ''
		  FROM urtuu_outbox o
		 WHERE d.outbox_id = o.id AND d.peer_id = $1 AND d.delivered_at IS NULL
		   AND o.message_id = ANY($2)`, peer.ID, messageIDs)
	if err == nil && tag.RowsAffected() > 0 {
		// Counted here rather than where the envelope was sent, because this is
		// the moment it is actually settled: the other side has said it holds
		// it. A count taken at the send would include every attempt that was
		// then retried.
		deliveriesTotal.WithLabelValues(deliveryOK).Add(float64(tag.RowsAffected()))
	}
	return err
}

// noteFailure records why a link is not moving, on the link itself. The screen
// reads this: "not delivered" without a reason is a support ticket.
func (s *Service) noteFailure(ctx context.Context, peer peerRow, reason string) {
	deliveriesTotal.WithLabelValues(deliveryFailed).Inc()
	_, _ = s.db.Exec(nexus.WithWorkspaceID(ctx, peer.TenantID),
		`UPDATE urtuu_peers SET last_error = $2, updated_at = NOW() WHERE id = $1`, peer.ID, reason)
}

// noteSeen records that the other side spoke, and by how much its clock
// disagrees.
//
// Skew is stored rather than acted on: SLAs are measured from the sender's
// stamp by design (§9), so the operator is told about a drifting clock instead
// of the platform quietly correcting for one. A nil skew means this exchange
// carried no envelope to measure against, and leaves the last measurement
// alone — an empty poll is not evidence that two clocks have come back into
// agreement.
func (s *Service) noteSeen(ctx context.Context, peer peerRow, skew *time.Duration) {
	var seconds *int
	if skew != nil {
		value := int(skew.Seconds())
		seconds = &value
	}
	_, _ = s.db.Exec(nexus.WithWorkspaceID(ctx, peer.TenantID), `
		UPDATE urtuu_peers
		   SET last_seen_at = NOW(), last_error = '',
		       clock_skew_seconds = COALESCE($2, clock_skew_seconds), updated_at = NOW()
		 WHERE id = $1`, peer.ID, seconds)
}

// receive stores one verified envelope, идемпотент by message id.
//
// The bool is false when the message was already held, which is the ordinary
// outcome of a retry rather than an error: the sender's response was lost, not
// its message.
func (s *Service) receive(ctx context.Context, peer peerRow, envelope contract.Envelope) (bool, error) {
	created, err := envelope.Time()
	if err != nil {
		return false, err
	}
	tag, err := s.db.Exec(nexus.WithWorkspaceID(ctx, peer.TenantID), `
		INSERT INTO urtuu_inbox (tenant_id, peer_id, message_id, kind, created_at, payload, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, message_id) DO NOTHING`,
		peer.TenantID, peer.ID, envelope.MessageID, envelope.Kind, created,
		string(envelope.Payload), envelope.Signature)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// unacked lists what has been stored from a link but not yet acknowledged to
// it. Until the acknowledgement lands the other side keeps offering the same
// envelopes, which is exactly what should happen.
func (s *Service) unacked(ctx context.Context, peer peerRow, limit int) ([]string, error) {
	rows, err := s.db.Query(nexus.WithWorkspaceID(ctx, peer.TenantID), `
		SELECT message_id FROM urtuu_inbox
		 WHERE peer_id = $1 AND acked_at IS NULL
		 ORDER BY received_at LIMIT $2`, peer.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// markAcked records that the other side has been told.
func (s *Service) markAcked(ctx context.Context, peer peerRow, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	_, err := s.db.Exec(nexus.WithWorkspaceID(ctx, peer.TenantID), `
		UPDATE urtuu_inbox SET acked_at = NOW()
		 WHERE peer_id = $1 AND acked_at IS NULL AND message_id = ANY($2)`, peer.ID, messageIDs)
	return err
}

// pendingCount answers whether a long poll has anything to return yet.
func (s *Service) pendingCount(ctx context.Context, peer peerRow) (int, error) {
	var count int
	err := s.db.QueryRow(nexus.WithWorkspaceID(ctx, peer.TenantID), `
		SELECT count(*) FROM urtuu_deliveries
		 WHERE peer_id = $1 AND delivered_at IS NULL AND next_attempt_at <= NOW()`, peer.ID).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return count, err
}
