/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The exchange: two endpoints the parent serves and one loop the child runs.
 *
 * Only the child ever dials (§2.1), so this file has two halves that never meet
 * on one installation for one link. The parent answers pull and push; the child
 * calls them. An installation in the middle of a chain does both, for different
 * links.
 */

package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/jackc/pgx/v5"
)

const (
	// pullWindow is how long a poll is held open with nothing to say.
	//
	// It is 25 seconds because eid.PollWindow is, and the server's write
	// deadline is derived from that one (pkg/host/run.go): a handler that
	// outlived the deadline would have its connection closed mid-answer and
	// the caller would see a 502 rather than an empty batch. Reusing the number
	// rather than picking a new one is what keeps the two from drifting apart.
	pullWindow = 25 * time.Second

	// pullTick is how often a held-open poll looks again. A second is
	// imperceptible against work measured in days and costs one cheap indexed
	// count per waiting link.
	pullTick = time.Second

	// batchLimit bounds one exchange in either direction. A child that has been
	// off for a week catches up over several cycles rather than in one request
	// nobody can time out safely.
	batchLimit = 50

	// maxBatchBytes bounds a pushed body. batchLimit envelopes at the contract's
	// own payload ceiling, plus room for the JSON around them.
	maxBatchBytes = batchLimit*contract.MaxPayloadBytes + (1 << 20)
)

// ------------------------------------------------------------ parent side

// authenticate resolves which link is speaking.
//
// Two different refusals, and the difference matters: 401 is "this token names
// no live link" — unknown, or revoked, which is the same answer on purpose —
// and 403 is "the link exists but its administrator has not confirmed it yet".
// A revoked peer must not be able to tell itself apart from one that was never
// there.
func (s *Service) authenticate(w http.ResponseWriter, r *http.Request) (peerRow, bool) {
	if !s.Enabled() {
		nexus.Error(w, http.StatusNotFound, "this installation does not run Өртөө")
		return peerRow{}, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		nexus.Error(w, http.StatusUnauthorized, "unauthorized")
		return peerRow{}, false
	}

	peer, err := s.peerByToken(r.Context(), token)
	if errors.Is(err, pgx.ErrNoRows) {
		nexus.Error(w, http.StatusUnauthorized, "unauthorized")
		return peerRow{}, false
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not resolve the link")
		return peerRow{}, false
	}
	if peer.Status != "active" {
		nexus.Error(w, http.StatusForbidden, "this link has not been confirmed yet")
		return peerRow{}, false
	}
	return peer, true
}

// HandlePull hands a child what is queued for it, holding the connection open
// for a while rather than answering an empty batch immediately.
func (s *Service) HandlePull(w http.ResponseWriter, r *http.Request) {
	peer, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	// How long the caller is willing to wait, clamped to what this side will
	// hold a connection open for. A client that wants an immediate answer —
	// an operator pressing "sync now", a test — asks for zero and gets one;
	// the exchange loop asks for the full window and the connection is held.
	deadline := time.Now().Add(waitFor(r))
	for {
		count, err := s.pendingCount(r.Context(), peer)
		if err != nil {
			nexus.Error(w, http.StatusInternalServerError, "could not read the queue")
			return
		}
		if count > 0 || time.Now().After(deadline) {
			break
		}
		select {
		case <-r.Context().Done():
			// The child hung up or the server is shutting down. Nothing has
			// been taken from the queue yet, so there is nothing to put back.
			return
		case <-time.After(pullTick):
		}
	}

	envelopes, err := s.dueEnvelopes(r.Context(), peer, batchLimit)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the queue")
		return
	}
	s.noteSeen(r.Context(), peer, nil)
	if len(envelopes) > 0 {
		nexus.Audit(nexus.WithWorkspaceID(r.Context(), peer.TenantID), peer.TenantID, "",
			"urtuu.sent", "urtuu_peer",
			map[string]any{"peer_id": peer.ID, "peer_name": peer.Name, "count": len(envelopes)})
	}
	nexus.JSON(w, http.StatusOK, contract.Batch{Envelopes: envelopes})
}

// HandlePush takes what a child reports and the acknowledgements it owes.
func (s *Service) HandlePush(w http.ResponseWriter, r *http.Request) {
	peer, ok := s.authenticate(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBatchBytes)
	var batch contract.Batch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid batch")
		return
	}
	if len(batch.Envelopes) > batchLimit {
		nexus.Error(w, http.StatusRequestEntityTooLarge, "too many envelopes in one batch")
		return
	}

	// The acknowledgements first: they are the cheap half, and a batch whose
	// envelopes are refused should still settle the deliveries the child has
	// already stored.
	if err := s.markDelivered(r.Context(), peer, batch.Ack); err != nil {
		slog.Warn("urtuu: could not settle acknowledged deliveries", "peer_id", peer.ID, "error", err)
	}

	accepted, skew, err := s.accept(r.Context(), peer, batch.Envelopes)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	// Answering means acknowledging: whatever is named in the response is
	// something this side is durably holding, so it is acknowledged here in the
	// same breath rather than waiting for a round trip that will not come.
	if err := s.markAcked(r.Context(), peer, accepted); err != nil {
		slog.Warn("urtuu: could not mark received envelopes acknowledged", "peer_id", peer.ID, "error", err)
	}
	s.noteSeen(r.Context(), peer, skew)

	nexus.JSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

// accept verifies and stores a batch, returning the ids it now holds.
//
// One bad envelope fails the whole batch. That is deliberate: a peer sending
// something that does not verify is either broken or not the peer, and taking
// the rest of what it said would be deciding that a forgery is a per-message
// problem. The child will resend; nothing is lost.
func (s *Service) accept(ctx context.Context, peer peerRow, envelopes []contract.Envelope) ([]string, *time.Duration, error) {
	now := time.Now()
	var skew *time.Duration
	accepted := make([]string, 0, len(envelopes))

	for _, envelope := range envelopes {
		if err := envelope.Valid(); err != nil {
			return nil, nil, err
		}
		// Signature before freshness, and both before the payload is looked at
		// by anybody: an unverified envelope's created_at is not evidence of
		// anything.
		if err := contract.Verify(peer.PublicKey, envelope); err != nil {
			nexus.Audit(nexus.WithWorkspaceID(ctx, peer.TenantID), peer.TenantID, "",
				"urtuu.rejected", "urtuu_peer",
				map[string]any{"peer_id": peer.ID, "message_id": envelope.MessageID, "reason": "signature"})
			return nil, nil, err
		}
		if err := envelope.Fresh(now); err != nil {
			return nil, nil, err
		}
		if created, err := envelope.Time(); err == nil {
			difference := created.Sub(now)
			skew = &difference
		}

		stored, err := s.receive(ctx, peer, envelope)
		if err != nil {
			return nil, nil, err
		}
		// Held either way. A repeat is acknowledged with the same answer the
		// first delivery got, which is what makes the retry safe to make.
		accepted = append(accepted, envelope.MessageID)
		if stored {
			nexus.Audit(nexus.WithWorkspaceID(ctx, peer.TenantID), peer.TenantID, "",
				"urtuu.received", "urtuu_peer",
				map[string]any{"peer_id": peer.ID, "peer_name": peer.Name,
					"message_id": envelope.MessageID, "kind": envelope.Kind})
		}
	}
	return accepted, skew, nil
}

// ------------------------------------------------------------- child side

// exchangeOnce runs one conversation with a parent: report, then listen.
//
// Push first so that acknowledgements owed from the previous cycle are settled
// before the parent decides what to offer again — otherwise every cycle would
// be handed the same envelopes until one happened to push.
func (s *Service) exchangeOnce(ctx context.Context, peer peerRow, wait time.Duration) error {
	if err := s.push(ctx, peer); err != nil {
		return err
	}
	return s.pull(ctx, peer, wait)
}

// waitFor reads the poll window the caller asked for, in seconds.
//
// Absent or unreadable means the full window, which is what every version of
// this client sends and what a peer that has not been upgraded will keep
// sending. Above the window is clamped: how long this installation will hold a
// connection open is its own decision, not the caller's.
func waitFor(r *http.Request) time.Duration {
	raw := strings.TrimSpace(r.URL.Query().Get("wait"))
	if raw == "" {
		return pullWindow
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 {
		return pullWindow
	}
	if asked := time.Duration(seconds) * time.Second; asked < pullWindow {
		return asked
	}
	return pullWindow
}

func (s *Service) push(ctx context.Context, peer peerRow) error {
	envelopes, err := s.dueEnvelopes(ctx, peer, batchLimit)
	if err != nil {
		return err
	}
	acks, err := s.unacked(ctx, peer, batchLimit)
	if err != nil {
		return err
	}
	if len(envelopes) == 0 && len(acks) == 0 {
		return nil
	}

	body, err := json.Marshal(contract.Batch{Envelopes: envelopes, Ack: acks})
	if err != nil {
		return err
	}
	response, err := s.call(ctx, peer, http.MethodPost, "/api/v1/urtuu/exchange/push", body)
	if err != nil {
		return err
	}

	var answer struct {
		Accepted []string `json:"accepted"`
	}
	if err := json.Unmarshal(response, &answer); err != nil {
		return err
	}
	if err := s.markDelivered(ctx, peer, answer.Accepted); err != nil {
		return err
	}
	// The parent settled these when it answered, so this side can stop
	// repeating them.
	return s.markAcked(ctx, peer, acks)
}

func (s *Service) pull(ctx context.Context, peer peerRow, wait time.Duration) error {
	response, err := s.call(ctx, peer, http.MethodGet,
		fmt.Sprintf("/api/v1/urtuu/exchange/pull?wait=%d", int(wait.Seconds())), nil)
	if err != nil {
		return err
	}
	var batch contract.Batch
	if err := json.Unmarshal(response, &batch); err != nil {
		return err
	}
	if len(batch.Envelopes) == 0 {
		s.noteSeen(ctx, peer, nil)
		return nil
	}

	// Verified on this side too. The token proved the connection reached the
	// installation the link names; the signature is what proves the parent
	// wrote what arrived.
	_, skew, err := s.accept(ctx, peer, batch.Envelopes)
	if err != nil {
		return err
	}
	s.noteSeen(ctx, peer, skew)
	return nil
}

// call makes one authenticated request to a parent.
func (s *Service) call(ctx context.Context, peer peerRow, method, path string, body []byte) ([]byte, error) {
	// A little more than the parent will hold a poll open for, so an empty
	// answer arrives as an empty answer rather than as a timeout.
	ctx, cancel := context.WithTimeout(ctx, pullWindow+10*time.Second)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, peer.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.peerToken(peer.ID))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		// The parent's own words, bounded. A link that has stopped working with
		// nothing but a status code behind it is a support ticket; the reason
		// is what the administrator reads off the links screen.
		reason, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("the parent answered %s: %s", res.Status, strings.TrimSpace(string(reason)))
	}
	return io.ReadAll(io.LimitReader(res.Body, maxBatchBytes))
}
