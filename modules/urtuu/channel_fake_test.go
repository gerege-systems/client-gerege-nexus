/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package urtuu

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
)

// The platform's channel, in memory.
//
// These tests stood up the real transport — internal/platform/urtuu, two HTTP
// servers, signing keys, an exchange loop — while this app lived in the core.
// That package is under internal/ and could not come here, and it should not
// have: what it holds is the platform's to test. What is this app's is what it
// does with work that arrives and what it puts on the wire, and both are
// visible through the five methods of contract.Link.
//
// So the two installations are two modules in one process, and an envelope is a
// slice entry. Everything that made the real channel a channel — signatures,
// retries, acknowledgement, clock skew — is deliberately absent: none of it is
// this app's behaviour, and a fake that reimplemented it would be asserting the
// fake.
type fakeChannel struct {
	mu sync.Mutex
	t  *testing.T

	installationID string
	// name is what the *other* side sees as the sender.
	name string
	// tenantID is the organisation this installation acts for. One tenant per
	// installation here; the real channel allows several.
	tenantID string
	// peerID is the row id by which this installation knows the other one. It
	// is a real row in urtuu_peers, because a task's origin_peer_id is a
	// foreign key into it — the channel's table is still the channel's.
	peerID string

	readers map[string]contract.LinkReader
	inbox   []contract.LinkMessage
	// read is everything that has been handed to a reader. The real channel
	// keeps its inbox rows after acknowledging them, and more than one test
	// here asks what came back rather than what the board then showed.
	read []contract.LinkMessage
	// spokeAt is when this link last carried something, which is what the board
	// shows as "the link is alive".
	spokeAt *time.Time

	other *fakeChannel
	// codes are the request codes opened on this link, as CodeOpenOn sees them.
	codes map[string]contract.RequestCode
	// local are codes this installation knows and has not announced anywhere.
	local map[string]contract.RequestCode
}

func (c *fakeChannel) Enabled() bool          { return true }
func (c *fakeChannel) InstallationID() string { return c.installationID }

func (c *fakeChannel) Deliver(kind string, reader contract.LinkReader) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readers == nil {
		c.readers = map[string]contract.LinkReader{}
	}
	c.readers[kind] = reader
}

// Enqueue puts a message in the other side's inbox.
//
// It does not deliver, exactly like the real one: carry is what hands an
// envelope to a reader. The difference matters to more than one test here —
// a task and the announcement of it are written in one transaction, and what
// the other end sees must be what was committed.
func (c *fakeChannel) Enqueue(_ context.Context, tenantID, kind string, payload any, _ ...string) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	message := contract.LinkMessage{
		WorkspaceID: c.other.tenantID,
		PeerID:      c.other.peerID,
		PeerName:    c.name,
		MessageID:   uuid.NewString(),
		Kind:        kind,
		CreatedAt:   time.Now(),
		Payload:     body,
	}
	_ = tenantID
	c.other.mu.Lock()
	c.other.inbox = append(c.other.inbox, message)
	c.other.mu.Unlock()
	return message.MessageID, nil
}

// EnqueueTx is Enqueue inside the caller's transaction — here, the same thing.
//
// The real one writes the outbox row in that transaction so that a rolled-back
// task cannot announce itself. Reproducing that would mean reproducing the
// outbox; what these tests need is that the app *calls* this one where it
// should, which the callers show.
func (c *fakeChannel) EnqueueTx(ctx context.Context, _ pgx.Tx, tenantID, kind string, payload any, peerIDs ...string) (string, error) {
	return c.Enqueue(ctx, tenantID, kind, payload, peerIDs...)
}

// Peers, RequestCode, CodeOpenOn and DeliveryLoad are contract.PeerDirectory: the
// reading half of the channel. The app asks for names and codes through it
// rather than joining the channel's tables — see peers.go and ADR 0004.
func (c *fakeChannel) Peers(context.Context, string) ([]contract.Peer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []contract.Peer{{
		ID: c.peerID, Name: c.other.name, Role: "child", Status: "active",
		LastSeenAt: c.spokeAt, Undelivered: len(c.other.inbox),
	}}, nil
}

// defineLocally is a code this installation knows and has *not* opened on the
// link. The difference is the whole of TestWorkCannotBeSentUnderACodeTheLinkWasNeverGiven:
// a code can be defined here and still be something the other end was never
// told about.
func (c *fakeChannel) defineLocally(code, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.local == nil {
		c.local = map[string]contract.RequestCode{}
	}
	c.local[code] = contract.RequestCode{
		Code: code, Names: map[string]string{"mn": "Зөвхөн дотоод"},
		Line: line, Active: true, Source: "local",
	}
}

func (c *fakeChannel) RequestCode(_ context.Context, _, code string) (contract.RequestCode, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if found, ok := c.codes[code]; ok {
		return found, true, nil
	}
	found, ok := c.local[code]
	return found, ok, nil
}

func (c *fakeChannel) CodeOpenOn(_ context.Context, _, _, code string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, open := c.codes[code]
	return open, nil
}

func (c *fakeChannel) DeliveryLoad(context.Context, string, time.Time, time.Time) ([]contract.PeerLoad, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []contract.PeerLoad{{
		PeerID: c.peerID, Envelopes: int64(len(c.other.inbox)), Delivered: 0,
		Pending: int64(len(c.other.inbox)),
	}}, nil
}

// openCode announces a code on the link, which is what a parent does before
// sending work under it — four requests to the channel's own routes in
// production, one map entry here.
func (c *fakeChannel) openCode(code, line string, sla int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.codes == nil {
		c.codes = map[string]contract.RequestCode{}
	}
	c.codes[code] = contract.RequestCode{
		Code:       code,
		Names:      map[string]string{"mn": "Тооллого", "en": "Count"},
		DefaultSLA: time.Duration(sla) * time.Second,
		Line:       line,
		Active:     true,
		Source:     "local",
	}
}

// drain hands everything waiting to the readers that were registered for it.
//
// A message with no reader is dropped rather than retried: on the real channel
// it would sit in the inbox until a module that reads that kind is compiled in,
// and no test here is about that.
func (c *fakeChannel) drain() {
	c.mu.Lock()
	waiting := c.inbox
	c.inbox = nil
	readers := make(map[string]contract.LinkReader, len(c.readers))
	for kind, reader := range c.readers {
		readers[kind] = reader
	}
	c.mu.Unlock()

	for _, message := range waiting {
		reader, ok := readers[message.Kind]
		if !ok {
			continue
		}
		if err := reader(context.Background(), message); err != nil {
			c.t.Errorf("reading a %s envelope: %v", message.Kind, err)
		}
		// Both ends have now spoken, not only the one that read: an envelope
		// that crossed is what "the link is alive" means, and the sender's
		// board is usually the one somebody is watching.
		spoke := time.Now()
		c.mu.Lock()
		c.read = append(c.read, message)
		c.spokeAt = &spoke
		c.mu.Unlock()
		c.other.mu.Lock()
		c.other.spokeAt = &spoke
		c.other.mu.Unlock()
	}
}

// received returns everything of one kind that arrived here, read or not.
//
// Read included, because that is what the real inbox does: a row is marked
// processed, not deleted, and the tests that ask what came back run after the
// envelope has been carried.
func (c *fakeChannel) received(kind string) []contract.LinkMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []contract.LinkMessage
	for _, message := range append(append([]contract.LinkMessage{}, c.read...), c.inbox...) {
		if message.Kind == kind {
			found = append(found, message)
		}
	}
	return found
}

// peerRow writes the channel's own row for a link.
//
// A child link needs an address the parent could dial, so one is given: the
// channel checks it, and nothing here ever calls it.
//
// The app never reads this table — that is the whole point of
// contract.PeerDirectory — but a task's origin_peer_id and target_peer_id are
// foreign keys into it, and a foreign key is not something a fake can stand in
// for. So the row exists and everything about it that a screen would show comes
// from the fake instead.
func peerRow(t *testing.T, pool *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO urtuu_peers (id, tenant_id, name, installation_id, role, status, base_url)
		VALUES ($1, $2, $3, $4, 'child', 'active', $5)`,
		id, tenantID, name, uuid.NewString(), "https://peer.invalid"); err != nil {
		t.Fatalf("create the channel's peer row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM urtuu_peers WHERE id = $1`, id)
	})
	return id
}
