/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package wire

import (
	"context"
	"time"
)

// ------------------------------------------------------------------ the ring

// PeerDirectory is what the board may ask about this installation's links.
//
// Link is how the board *uses* the channel. This is how it *reads* it: the
// names to put beside a task, whether a code has been announced on a link, what
// a code means. Three questions a screen about work exchanged with another
// installation cannot avoid asking and that the board must not answer by
// reading the channel's tables.
//
// Which is what it did until 2026-08-23, joining urtuu_peers in nine queries
// and reading urtuu_request_codes and urtuu_peer_codes in two more, on the
// argument — written down beside the code — that "the two packages are one
// product split by layer, sharing one schema". Both halves live in one
// repository again, which is precisely why the boundary is worth keeping
// written down: the SQL is the channel's, the questions are the board's, and
// the day the board reaches past this interface is the day the split stops
// meaning anything.
//
// The shape follows nexus.Directory: one read per page, and the caller maps ids
// to names in memory. A per-row accessor would turn a join into N queries,
// which is the wrong way to pay for a boundary.
type PeerDirectory interface {
	// Peers returns this organisation's links, with the state a screen shows.
	//
	// Revoked links are not returned: a revoked link carries nothing and a
	// screen that listed it would be offering somebody a peer to send to.
	Peers(ctx context.Context, workspaceID string) ([]Peer, error)

	// RequestCode returns what a code means to this installation, and false if
	// this installation has never been told.
	RequestCode(ctx context.Context, workspaceID, code string) (RequestCode, bool, error)

	// CodeOpenOn reports whether a code has been announced on one link.
	//
	// A parent that has not opened a code on a link must not send work under
	// it: the other end would receive a task naming a code nobody told it
	// about, and announcing the vocabulary is what stops it having to guess.
	CodeOpenOn(ctx context.Context, workspaceID, peerID, code string) (bool, error)

	// DeliveryLoad is what actually went over each link in a period.
	//
	// Here rather than left as a join because the alternative is a module
	// reading urtuu_deliveries, and a count of envelopes is the one question
	// about the channel that a report can ask and Peers cannot answer: Peers
	// says what is stuck now, this says what moved between two dates.
	DeliveryLoad(ctx context.Context, workspaceID string, from, to time.Time) ([]PeerLoad, error)
}

// PeerLoad is one link's traffic over a period.
type PeerLoad struct {
	PeerID    string `json:"peer_id"`
	Envelopes int64  `json:"envelopes"`
	Delivered int64  `json:"delivered"`
	Pending   int64  `json:"pending"`
	// Retries counts attempts beyond the first. An envelope that went first
	// time has one attempt and no retry — counting it as one would make a
	// healthy channel look like a struggling one.
	Retries int64 `json:"retries"`
}

// Peer is one link, as a screen about work sees it.
//
// Narrower than the channel's own record on purpose. The rail's peer carries
// the base URL, the public key, the invitation's expiry and the clock skew —
// which are an administrator's business on the links screen, and none of a task
// board's. A contract that carried all fourteen fields would make every one of
// them a promise to keep.
type Peer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is what this installation is on the link, from here: the direction
	// rather than a rank.
	Role   string `json:"role"`
	Status string `json:"status"`
	// LastSeenAt and LastError are the two halves of "is this link working".
	// Nil and empty on a link that has never been used, which is not an error.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	// Undelivered is how much is waiting in the outbox for this peer. It is the
	// number that turns "the link is fine" into "the link has been fine for an
	// hour and nothing has moved".
	Undelivered int `json:"undelivered"`
}

// RequestCode, the type the reader above answers with, is status.go's — the
// same record the vocabulary travels under on the wire. There were two of these
// while the channel was the platform's and the board was an app: the platform
// published a six-field reading of the code beside the contract's own eleven.
// One repository, one type: the fields the board does not read cost it nothing,
// and a second definition of "what a code is" is how the two come to disagree
// about the one that matters.
