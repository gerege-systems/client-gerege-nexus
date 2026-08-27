/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package wire is what two Gerege Nexus installations say to each other when
 * one hands the other a piece of work.
 *
 * Өртөө — the relay-post network of the Mongol Empire. A dispatch was carried
 * from post to post, each one acknowledging what it took, and the answer came
 * back the same way. The shape of the problem has not changed: an installation
 * accepts a task, either settles it or passes it on, and reports back up.
 *
 * This package is the contract and nothing else — no database, no HTTP, no
 * configuration. It was backend/pkg/urtuu in the core, published so that every
 * distribution would agree on what an envelope is down to the byte. It came
 * here with the channel on 2026-08-27: the agreement is still the point, but
 * the byte-for-byte definition now travels with the only implementation of it,
 * and a second distribution that joins a ring takes this app from the
 * catalogue rather than inheriting an unused type from the platform.
 *
 * Three things live here, and they are three halves of one agreement: the
 * envelope on the network, the task vocabulary an envelope names, and the two
 * interfaces the board and the channel meet across (link.go, directory.go).
 *
 * The transport that carries these is modules/urtuu/channel; the task
 * lifecycle built on them is modules/urtuu.
 */
package wire

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Kinds of envelope. The kind decides who reads the payload, and nothing else:
// the transport neither parses nor validates one, which is what lets a new kind
// be added without every installation on the graph being upgraded first — an
// unknown kind is stored, acknowledged and left for a build that knows it.
const (
	// KindTaskAssigned is a parent handing a child a task.
	KindTaskAssigned = "task.assigned"
	// KindTaskUpdate is any state a child reports back about a task it was
	// given — accepted, returned, delegated onward, completed.
	KindTaskUpdate = "task.update"
	// KindCodeSync is the parent announcing which request codes it has opened
	// on this link.
	KindCodeSync = "code.sync"
)

const (
	// MaxPayloadBytes bounds one envelope. Tasks carry structured fields and a
	// reference to evidence, never the evidence itself — a megabyte is already
	// far past anything legitimate, and this is the ceiling that keeps a
	// hostile peer from being an out-of-memory.
	MaxPayloadBytes = 1 << 20

	// MaxAge is how old a signed envelope may be when it arrives.
	//
	// It is the other half of replay protection. Идемпотент receipt — one row
	// per message_id — is what stops the same envelope being acted on twice,
	// but only for as long as that row is kept; past that the record is pruned
	// and a captured envelope would be new again. So the window an envelope is
	// accepted in is bounded here, and the inbox is kept longer than it (see
	// internal/workspace/urtuu/housekeeping.go). Seven days is generous for a
	// child that was switched off for a week and still less than the retention
	// it is measured against.
	MaxAge = 7 * 24 * time.Hour

	// MaxSkew is how far into the future a peer's clock may be. Two
	// installations in different data centres do disagree by seconds; a peer
	// that disagrees by more than this is reported as a health problem rather
	// than silently having its deadlines believed.
	MaxSkew = 5 * time.Minute
)

// Envelope is one signed message between two installations.
//
// CreatedAt is a string and Payload is raw JSON for the same reason
// `pkg/catalog`'s signed document holds its apps that way: the signature covers
// bytes. Decoding and re-encoding either field to verify it would mean trusting
// this program's field order, number formatting and escaping to reproduce the
// sender's bytes exactly, which is how a signature check quietly stops checking
// anything. Whatever arrived is what is verified, stored and forwarded.
type Envelope struct {
	// MessageID is the идемпотент key. The receiver holds one row per id and
	// answers a repeat with the same acknowledgement it gave the first time, so
	// a delivery that succeeded but whose response was lost costs a duplicate
	// request and nothing else.
	MessageID string `json:"message_id"`
	Kind      string `json:"kind"`
	// CreatedAt is the sender's clock, in RFC 3339. It is inside the signature,
	// so a captured envelope cannot be re-dated, and it is what an SLA is
	// measured from — the receiver's clock is not the one the deadline was
	// promised against.
	CreatedAt string          `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}

// SigningInput is what is signed and what is verified:
//
//	created_at "\n" <the payload, verbatim>
//
// Byte-for-byte the shape `pkg/catalog` uses. Repeating a proven layout is
// worth more here than any improvement on it: the two are read together, and a
// reviewer who has understood one has understood both.
func (e Envelope) SigningInput() []byte {
	message := make([]byte, 0, len(e.CreatedAt)+1+len(e.Payload))
	message = append(message, e.CreatedAt...)
	message = append(message, '\n')
	return append(message, e.Payload...)
}

// New builds an unsigned envelope carrying payload.
//
// The payload is marshalled once, here, and never again: from this point it is
// bytes, and the bytes are what the signature will cover.
func New(messageID, kind string, createdAt time.Time, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("urtuu: marshal payload: %w", err)
	}
	if len(raw) > MaxPayloadBytes {
		return Envelope{}, fmt.Errorf("urtuu: payload is %d bytes, over the %d limit", len(raw), MaxPayloadBytes)
	}
	return Envelope{
		MessageID: messageID,
		Kind:      kind,
		// UTC and RFC3339Nano, always. The format is inside the signature, so
		// two installations formatting the same instant differently would
		// produce envelopes that verify on neither side.
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
		Payload:   raw,
	}, nil
}

// Sign fills in Signature. The key is the sending installation's own —
// URTUU_SIGNING_KEY — not the link's.
func Sign(key ed25519.PrivateKey, envelope Envelope) (Envelope, error) {
	if len(key) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("urtuu: signing key is not an Ed25519 private key")
	}
	envelope.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, envelope.SigningInput()))
	return envelope, nil
}

// ErrSignature is what a failed verification returns, so a caller can tell a
// forged envelope from a malformed one without matching on message text.
var ErrSignature = errors.New("urtuu: envelope signature does not verify")

// Verify checks an envelope against the public key exchanged when the link was
// established.
//
// A signature that does not check out is not a reason to read the envelope more
// carefully — it is a reason to stop reading it. Nothing from an unverified
// envelope reaches the rest of the platform, not even the kind.
func Verify(key ed25519.PublicKey, envelope Envelope) error {
	if len(key) != ed25519.PublicKeySize {
		return errors.New("urtuu: peer public key is not an Ed25519 public key")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return fmt.Errorf("urtuu: decode signature: %w", err)
	}
	if !ed25519.Verify(key, envelope.SigningInput(), signature) {
		return ErrSignature
	}
	return nil
}

// Time is the sender's stamp as an instant.
func (e Envelope) Time() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, e.CreatedAt)
}

// Fresh reports whether an envelope arrived inside the window it may be acted
// on in — see MaxAge for why there is a window at all.
func (e Envelope) Fresh(now time.Time) error {
	created, err := e.Time()
	if err != nil {
		return fmt.Errorf("urtuu: created_at is not RFC 3339: %w", err)
	}
	if now.Sub(created) > MaxAge {
		return fmt.Errorf("urtuu: envelope is %s old, past the %s limit", now.Sub(created).Round(time.Second), MaxAge)
	}
	if created.Sub(now) > MaxSkew {
		return fmt.Errorf("urtuu: envelope is dated %s in the future", created.Sub(now).Round(time.Second))
	}
	return nil
}

// Valid checks the shape of an envelope before its signature is even looked at.
// Cheap refusals first: a peer that can make this platform allocate a megabyte
// per request has found a denial of service that costs it nothing.
func (e Envelope) Valid() error {
	switch {
	case e.MessageID == "":
		return errors.New("urtuu: envelope has no message_id")
	case e.Kind == "":
		return errors.New("urtuu: envelope has no kind")
	case len(e.Payload) == 0:
		return errors.New("urtuu: envelope has no payload")
	case len(e.Payload) > MaxPayloadBytes:
		return fmt.Errorf("urtuu: payload is %d bytes, over the %d limit", len(e.Payload), MaxPayloadBytes)
	case e.Signature == "":
		return errors.New("urtuu: envelope is unsigned")
	}
	return nil
}

// Batch is what one exchange carries in either direction.
//
// Envelopes and acknowledgements travel together because a child's half of a
// conversation is always both: "here is what happened here, and here is what I
// have already taken from you". Splitting them into two endpoints would double
// the round trips and leave the two halves able to disagree about what a peer
// has seen.
type Batch struct {
	Envelopes []Envelope `json:"envelopes"`
	// Ack lists message ids the sender has durably stored. The other side marks
	// those deliveries done; anything unacknowledged is offered again.
	Ack []string `json:"ack,omitempty"`
}
