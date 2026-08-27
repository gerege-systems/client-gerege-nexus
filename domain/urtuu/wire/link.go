/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package wire

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sending something to another installation, and being handed what arrives.
//
// This was nexus.Link, published by the platform, on the argument that one
// signing key, one outbox, one retry schedule and one set of peers belong to an
// *installation* rather than to an app. The argument was sound and the premise
// was not: in three months the ring had exactly one caller — the task board
// beside this package — so what the platform was publishing was one app's
// transport, and every deployment that never joined a ring carried it anyway.
//
// So the interface came here with the channel that implements it
// (modules/urtuu/channel) and the board that uses it (modules/urtuu). It stays
// an interface for the reason it was one: the board's tests stand two
// installations up as two structs in one process, and everything that makes the
// real channel a channel — signatures, retries, acknowledgement, clock skew —
// is deliberately absent from them.
//
// What travels on it is the caller's business: a task assignment, an update, a
// vocabulary announcement — the ring does not know what a task is, and that is
// deliberate. It carries a signed envelope of a named kind between two
// installations that have agreed to talk, and refuses everything else.
//
// # What is deliberately not here
//
// Peering. Who this installation is linked to, in which direction, with which
// key, is an operator's decision made in the console — not something a module
// can arrange for itself. A module that could add a peer could arrange its own
// audience, and the whole value of the ring is that both ends agreed first.
//
// Nor is delivery order, retry or acknowledgement. A module hands over a
// message and is told the id it will be known by; when it arrives, and how
// often the platform tried, is the transport's problem. A module that could
// influence that would be a module that could starve another one.
type Link interface {
	// Enabled reports whether this installation can send at all.
	//
	// False on every deployment that has not been given a signing key, which is
	// most of them: the ring is opt-in and an installation with no key is not
	// half-joined, it is not joined. A module should ask before offering a
	// screen whose buttons would all fail.
	Enabled() bool

	// InstallationID is what this deployment is called on the ring — the name
	// the other end sees as the sender.
	InstallationID() string

	// Enqueue hands a message to the outbox and returns the id it will be
	// known by at both ends.
	//
	// It does not send. The platform signs, delivers and retries on its own
	// schedule, so a caller that returns successfully has been promised the
	// message is *recorded*, not that it has arrived — the difference matters
	// on a link that is down, which is the case this exists for.
	//
	// With no peer ids the message goes to every peer this tenant is linked to
	// in the sending direction. Naming peers narrows it to those.
	Enqueue(ctx context.Context, workspaceID, kind string, payload any, peerIDs ...string) (string, error)

	// EnqueueTx is Enqueue inside the caller's transaction.
	//
	// This is the one that is usually right. A task written to a module's own
	// table and a message announcing it are one fact: enqueuing outside the
	// transaction that writes the row produces an announcement of something
	// that never existed when the transaction rolls back, and there is nothing
	// downstream that can tell.
	EnqueueTx(ctx context.Context, tx pgx.Tx, workspaceID, kind string, payload any, peerIDs ...string) (string, error)

	// Deliver registers the reader for one kind of arriving message.
	//
	// Called during construction, once per kind. One reader per kind by design:
	// two would make "was this processed" a question with two answers.
	//
	// Returning an error leaves the envelope unprocessed and it is offered
	// again on the next round, so a reader that fails because the database was
	// briefly away does not lose the work. A reader that fails because the
	// message is nonsense will be offered it for ever, which is the right
	// trade: the platform cannot tell those apart, and losing work quietly is
	// worse than retrying it loudly.
	Deliver(kind string, reader LinkReader)
}

// LinkReader consumes one arriving message.
type LinkReader func(ctx context.Context, message LinkMessage) error

// LinkMessage is one envelope as the receiving module sees it.
type LinkMessage struct {
	WorkspaceID string
	PeerID      string
	PeerName    string
	MessageID   string
	Kind        string
	// CreatedAt is the *sender's* clock. Deadlines are measured from it by
	// design: the receiving installation's clock is not the one the work was
	// promised against.
	CreatedAt time.Time
	Payload   []byte
}
