/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package channel

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// AsPeerDirectory presents the channel's links as contract.PeerDirectory.
//
// The three questions a module about exchanged work has to ask — who is on the
// other end, what does this code mean, has it been announced on that link —
// answered here rather than by a module reading urtuu_peers, urtuu_request_codes
// and urtuu_peer_codes itself. The task board did exactly that until
// 2026-08-23; see the contract's own comment and ADR 0004.
func AsPeerDirectory(s *Service) contract.PeerDirectory { return peerDirectory{s} }

type peerDirectory struct{ s *Service }

// Peers narrows the channel's record to what a screen about work needs.
//
// listPeers already excludes nothing, so the revoked ones are dropped here:
// a revoked link carries no message, and offering it as a destination is how
// somebody sends work into a link that was closed for a reason.
func (p peerDirectory) Peers(ctx context.Context, tenantID string) ([]contract.Peer, error) {
	peers, err := p.s.listPeers(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]contract.Peer, 0, len(peers))
	for _, peer := range peers {
		if peer.RevokedAt != nil {
			continue
		}
		out = append(out, contract.Peer{
			ID: peer.ID, Name: peer.Name, Role: peer.Role, Status: peer.Status,
			LastSeenAt: peer.LastSeenAt, LastError: peer.LastError,
			Undelivered: peer.Undelivered,
		})
	}
	return out, nil
}

// RequestCode reads one entry of the vocabulary.
//
// The tenant is bound on the context as well as named in the predicate, which
// is this repository's habit rather than belt and braces: the row-level policy
// is what actually keeps one organisation out of another's, and the predicate
// is what makes the query readable.
func (p peerDirectory) RequestCode(ctx context.Context, tenantID, code string) (contract.RequestCode, bool, error) {
	var found contract.RequestCode
	var sla *int64
	err := p.s.db.QueryRow(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT code, names, schema, EXTRACT(EPOCH FROM default_sla)::bigint, line,
		       source, ring_process_ref, version, active
		  FROM urtuu_request_codes WHERE tenant_id = $1 AND code = $2`,
		tenantID, code).Scan(&found.Code, &found.Names, &found.Schema, &sla, &found.Line,
		&found.Source, &found.RingProcessRef, &found.Version, &found.Active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Not an error: a code this installation has never been told about is
		// the ordinary answer for work arriving from a peer whose vocabulary
		// has not synced yet.
		return contract.RequestCode{}, false, nil
	case err != nil:
		return contract.RequestCode{}, false, err
	}
	// NULL means the code names no norm, and so does a zero Duration — the
	// column is nullable and the contract's field is not, which is the one
	// place the two spellings of "nobody promised anything" meet.
	if sla != nil {
		found.DefaultSLA = time.Duration(*sla) * time.Second
	}
	return found, true, nil
}

// DeliveryLoad counts the outbox by peer over a period.
//
// Grouped by peer id rather than by name: two links may be called the same
// thing, and a report that added them together would be reporting about
// neither.
func (p peerDirectory) DeliveryLoad(ctx context.Context, tenantID string, from, to time.Time) ([]contract.PeerLoad, error) {
	rows, err := p.s.db.Query(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT d.peer_id::text,
		       count(*),
		       count(*) FILTER (WHERE d.delivered_at IS NOT NULL),
		       count(*) FILTER (WHERE d.delivered_at IS NULL),
		       coalesce(sum(greatest(d.attempts - 1, 0)), 0)
		  FROM urtuu_deliveries d
		 WHERE d.tenant_id = $1 AND d.created_at >= $2 AND d.created_at <= $3
		 GROUP BY 1
		 ORDER BY 2 DESC
		 LIMIT 500`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	load := make([]contract.PeerLoad, 0, 16)
	for rows.Next() {
		var one contract.PeerLoad
		if err := rows.Scan(&one.PeerID, &one.Envelopes, &one.Delivered, &one.Pending, &one.Retries); err != nil {
			return nil, err
		}
		load = append(load, one)
	}
	return load, rows.Err()
}

func (p peerDirectory) CodeOpenOn(ctx context.Context, tenantID, peerID, code string) (bool, error) {
	var open bool
	err := p.s.db.QueryRow(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT EXISTS (SELECT 1 FROM urtuu_peer_codes
		                WHERE tenant_id = $1 AND peer_id = $2 AND code = $3)`,
		tenantID, peerID, code).Scan(&open)
	return open, err
}
