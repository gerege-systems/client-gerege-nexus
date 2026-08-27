/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Reading what arrived, and the first thing that reads it.
 *
 * The inbox is written by the transport and drained here. Kinds are dispatched
 * to whoever registered for them: the vocabulary announcement is this package's
 * own, and the task envelopes belong to the Өртөө app, which registers for them
 * when it is compiled in. An envelope of a kind nobody claims is left
 * unprocessed rather than discarded — a build that does not know a kind is not
 * evidence that the message was worthless, and the app being installed later is
 * exactly the case that has to keep working.
 */

package channel

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The message and the reader are pkg/nexus's shapes, not this package's.
//
// They were declared here and the Өртөө app imported them, which is fine
// while the app is compiled from this repository and impossible the moment it
// is not: a distribution cannot import internal/. Declaring them in the SDK
// costs this package two type names and buys the app the ability to leave.

// Deliver registers a reader for one kind.
//
// Called during construction — by this package for its own kind, and by the
// Өртөө app for the task kinds. One reader per kind: two would make "was this
// processed" a question with two answers.
func (s *Service) Deliver(kind string, reader contract.LinkReader) {
	s.readersMu.Lock()
	defer s.readersMu.Unlock()
	if s.readers == nil {
		s.readers = map[string]contract.LinkReader{}
	}
	s.readers[kind] = reader
}

func (s *Service) readerFor(kind string) (contract.LinkReader, bool) {
	s.readersMu.RLock()
	defer s.readersMu.RUnlock()
	reader, ok := s.readers[kind]
	return reader, ok
}

// inboxBatch bounds one drain. Small on purpose: a round that takes a long time
// is a round in which nothing else on the loop happens.
const inboxBatch = 100

// ProcessInbox hands every unread envelope to its reader.
//
// Exported because an installation at the top of a chain dials nobody: it has
// no child links, so an exchange round does nothing for it and reading what
// its subordinates have pushed is the whole of its half of the conversation.
// The loop calls it every round; ExchangeNow calls it too.
//
// It crosses organisations deliberately — an envelope belongs to whichever
// tenant's link brought it — so the listing runs on the platform path and each
// reader is called inside its own envelope's tenant.
func (s *Service) ProcessInbox(ctx context.Context) {
	rows, err := s.db.Query(nexus.WithoutWorkspace(ctx), `
		SELECT i.id::text, i.tenant_id::text, i.peer_id::text, coalesce(p.name, ''),
		       i.message_id, i.kind, i.created_at, i.payload
		  FROM urtuu_inbox i
		  JOIN urtuu_peers p ON p.id = i.peer_id
		 WHERE i.processed_at IS NULL AND p.installation_id = $1
		 ORDER BY i.received_at
		 LIMIT $2`, s.installationID, inboxBatch)
	if err != nil {
		slog.Warn("urtuu: could not read the inbox", "error", err)
		return
	}

	type pending struct {
		id      string
		message contract.LinkMessage
	}
	batch := make([]pending, 0, inboxBatch)
	for rows.Next() {
		var item pending
		var payload string
		if err := rows.Scan(&item.id, &item.message.WorkspaceID, &item.message.PeerID,
			&item.message.PeerName, &item.message.MessageID, &item.message.Kind,
			&item.message.CreatedAt, &payload); err != nil {
			rows.Close()
			slog.Warn("urtuu: could not read an inbox row", "error", err)
			return
		}
		item.message.Payload = []byte(payload)
		batch = append(batch, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("urtuu: could not read the inbox", "error", err)
		return
	}

	for _, item := range batch {
		reader, ok := s.readerFor(item.message.Kind)
		if !ok {
			// Left where it is. See the file comment: an unclaimed kind is a
			// build that has not caught up, not a message to throw away.
			continue
		}
		if err := reader(ctx, item.message); err != nil {
			slog.Warn("urtuu: could not process an envelope",
				"kind", item.message.Kind, "message_id", item.message.MessageID, "error", err)
			continue
		}
		if _, err := s.db.Exec(nexus.WithWorkspaceID(ctx, item.message.WorkspaceID),
			`UPDATE urtuu_inbox SET processed_at = NOW() WHERE id = $1`, item.id); err != nil {
			// The reader has already acted. Failing to record that costs a
			// second call, which is why every reader has to be safe to repeat.
			slog.Warn("urtuu: an envelope was processed but not marked",
				"message_id", item.message.MessageID, "error", err)
		}
	}
}

// codeSync is the payload of a KindCodeSync envelope: the complete set of codes
// a parent has opened on this link.
//
// A snapshot rather than a change list. A child that was switched off for two
// weeks would otherwise have to be sent every change since, in order, and
// getting that wrong leaves a vocabulary that is subtly not the parent's —
// whereas a snapshot that arrives out of order is settled by the version each
// code carries.
type codeSync struct {
	Codes []contract.RequestCode `json:"codes"`
}

// announceCodes tells one link what it may raise work under.
func (s *Service) announceCodes(ctx context.Context, tenantID, peerID string) error {
	rows, err := s.db.Query(nexus.WithWorkspaceID(ctx, tenantID), `
		SELECT c.code, c.names, c.schema, c.line,
		       coalesce(EXTRACT(EPOCH FROM c.default_sla)::bigint, 0),
		       c.ring_process_ref, c.version
		  FROM urtuu_peer_codes pc
		  JOIN urtuu_request_codes c
		    ON c.tenant_id = pc.tenant_id AND c.code = pc.code
		 WHERE pc.peer_id = $1 AND c.active
		 ORDER BY c.code`, peerID)
	if err != nil {
		return err
	}
	defer rows.Close()

	announcement := codeSync{Codes: make([]contract.RequestCode, 0, 32)}
	for rows.Next() {
		var code contract.RequestCode
		var seconds int64
		if err := rows.Scan(&code.Code, &code.Names, &code.Schema, &code.Line, &seconds,
			&code.RingProcessRef, &code.Version); err != nil {
			return err
		}
		code.DefaultSLA = time.Duration(seconds) * time.Second
		code.Active = true
		announcement.Codes = append(announcement.Codes, code)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Sent even when it is empty, because empty is a real announcement: it is
	// how a parent closes a vocabulary it had opened.
	_, err = s.Enqueue(ctx, tenantID, contract.KindCodeSync, announcement, peerID)
	return err
}

// receiveCodeSync stores what a parent announced.
//
// The snapshot replaces this link's whole vocabulary: codes in it are written
// or updated, and codes this link announced before and no longer does are
// removed. Only this link's codes are touched — a sibling parent's
// announcement, and anything authored here, are not that parent's to withdraw.
func (s *Service) receiveCodeSync(ctx context.Context, message contract.LinkMessage) error {
	var announcement codeSync
	if err := json.Unmarshal(message.Payload, &announcement); err != nil {
		// Malformed, from a verified sender. Marking it read is the right
		// outcome: retrying will not make it parse, and leaving it would put
		// the same failure in the log for ever.
		slog.Warn("urtuu: a code announcement could not be read",
			"peer_id", message.PeerID, "error", err)
		return nil
	}

	ctx = nexus.WithWorkspaceID(ctx, message.WorkspaceID)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	kept := make([]string, 0, len(announcement.Codes))
	for _, code := range announcement.Codes {
		if err := upsertCode(ctx, tx, message.WorkspaceID, contract.SourceLink, message.PeerID, code); err != nil {
			return err
		}
		kept = append(kept, code.Code)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM urtuu_request_codes WHERE source_peer_id = $1 AND NOT (code = ANY($2))`,
		message.PeerID, kept); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
