/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * What runs on its own: the exchange loop every child link needs, and the sweep
 * that stops three append-mostly tables growing for ever.
 */

package channel

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

const (
	// exchangePause is how long the loop waits after a round in which nothing
	// happened. A round in which a poll was held open has already taken up to
	// pullWindow, so this is not the polling interval — it is the floor under
	// a link that is failing fast, so a dead parent is retried a few times a
	// minute rather than as fast as its connection refusals come back.
	exchangePause = 5 * time.Second

	// exchangeConcurrency bounds how many parents are talked to at once. Each
	// held-open poll costs a goroutine and a connection for up to pullWindow,
	// and an installation with fifty parents must not spend fifty connections
	// waiting.
	exchangeConcurrency = 8

	// sweepInterval is the pace of the retention sweep. Hourly, like every
	// other sweep on this platform.
	sweepInterval = time.Hour

	// deliveredRetention is how long a settled delivery is kept. Six months, to
	// match integration's delivery log and for the same reason: "did that task
	// actually reach them" is asked months later.
	deliveredRetention = 180 * 24 * time.Hour

	// inboxRetention has to outlast contract.MaxAge by a wide margin, because the
	// two together are the replay defence: an envelope may be acted on for
	// MaxAge, and the row that says it already was must still be here when the
	// last acceptable copy of it could arrive. Six months against seven days.
	inboxRetention = 180 * 24 * time.Hour
)

// background starts fn in a goroutine and keeps a panic in it from taking the
// process with it.
//
// The platform's internal/kernel/async.Go, which this was, and which a module
// outside that repository cannot import. Twelve lines is the whole of it, and
// what matters is not the lines: it is that an exchange loop panicking on one
// malformed reply must not stop the deployment serving.
func background(name string, fn func()) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("background task panicked", "task", name,
					"panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

// StartHousekeeping runs the exchange loop and the retention sweep until ctx is
// cancelled. It returns immediately.
func (s *Service) StartHousekeeping(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	background("urtuu-exchange", func() { s.exchangeLoop(ctx) })
	background("urtuu-sweep", func() {
		// Once on the way in: a deployment that restarts more often than the
		// interval would otherwise never sweep.
		s.sweepOnce(ctx)
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepOnce(ctx)
			}
		}
	})
}

// exchangeLoop keeps every link this installation is the child on in
// conversation with its parent.
//
// One loop over all links rather than a goroutine per link with a supervisor
// around it: links are added and revoked by administrators, and re-reading the
// list each round is how a new one starts being served and a revoked one stops
// — with no bookkeeping to get wrong and nothing to leak when a link goes away.
func (s *Service) exchangeLoop(ctx context.Context) {
	for {
		links, err := s.activeChildLinks(ctx)
		if err != nil {
			slog.Warn("urtuu: could not read the links to poll", "error", err)
		}
		s.exchangeRound(ctx, links)
		// Draining the inbox is on this loop rather than a loop of its own
		// because it has to run on both sides: a parent's inbox is filled by a
		// child pushing, and a parent has no child links to poll. The round
		// happens whether or not there was anything to talk to.
		s.ProcessInbox(ctx)
		// The gauge an alert reads. On this loop rather than on the hourly
		// sweep: a number describing whether a link has gone quiet has to move
		// at the pace of the link.
		s.refreshPeerGauge(ctx)

		select {
		case <-ctx.Done():
			return
		case <-time.After(exchangePause):
		}
	}
}

// ExchangeNow runs one round without holding any connection open, and drains
// whatever it brought back.
//
// The background loop does the same work on its own schedule with the poll held
// open; this is for a caller that has to have caught up before it looks — an
// operator pressing "sync now" on the links screen, and the integration tests,
// which would otherwise spend twenty-five seconds proving an empty queue is
// empty.
func (s *Service) ExchangeNow(ctx context.Context) error {
	links, err := s.activeChildLinks(ctx)
	if err != nil {
		return err
	}
	var failure error
	for _, link := range links {
		if err := s.exchangeOnce(ctx, link, 0); err != nil {
			s.noteFailure(ctx, link, err.Error())
			failure = err
		}
	}
	s.ProcessInbox(ctx)
	return failure
}

func (s *Service) exchangeRound(ctx context.Context, links []peerRow) {
	slots := make(chan struct{}, exchangeConcurrency)
	var wait sync.WaitGroup
	for _, link := range links {
		select {
		case <-ctx.Done():
			return
		case slots <- struct{}{}:
		}
		wait.Add(1)
		go func(peer peerRow) {
			defer wait.Done()
			defer func() { <-slots }()
			if err := s.exchangeOnce(ctx, peer, pullWindow); err != nil && ctx.Err() == nil {
				// On the link rather than only in the log: an administrator
				// looking at Settings → Өртөө has to be able to see why
				// nothing is moving.
				s.noteFailure(ctx, peer, err.Error())
				slog.Warn("urtuu: exchange with a parent failed",
					"peer_id", peer.ID, "base_url", peer.BaseURL, "error", err)
			}
		}(link)
	}
	wait.Wait()
}

// sweepOnce prunes what has stopped being useful.
func (s *Service) sweepOnce(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Housekeeping crosses every organisation deliberately, so it runs on the
	// platform path — the same rule every other sweep on this platform follows.
	sweepCtx = nexus.WithoutWorkspace(sweepCtx)

	// Deliveries first, outbox second: an outbox row with a delivery still
	// pointing at it is one somebody is still waiting for, and the foreign key
	// says so.
	if tag, err := s.db.Exec(sweepCtx,
		`DELETE FROM urtuu_deliveries WHERE delivered_at IS NOT NULL AND delivered_at < $1`,
		time.Now().Add(-deliveredRetention)); err != nil {
		slog.Warn("urtuu: could not prune settled deliveries", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("urtuu: pruned settled deliveries", "count", tag.RowsAffected())
	}

	if tag, err := s.db.Exec(sweepCtx, `
		DELETE FROM urtuu_outbox o
		 WHERE o.queued_at < $1
		   AND NOT EXISTS (SELECT 1 FROM urtuu_deliveries d WHERE d.outbox_id = o.id)`,
		time.Now().Add(-deliveredRetention)); err != nil {
		slog.Warn("urtuu: could not prune the outbox", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("urtuu: pruned the outbox", "count", tag.RowsAffected())
	}

	if tag, err := s.db.Exec(sweepCtx,
		`DELETE FROM urtuu_inbox WHERE received_at < $1`,
		time.Now().Add(-inboxRetention)); err != nil {
		slog.Warn("urtuu: could not prune the inbox", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("urtuu: pruned the inbox", "count", tag.RowsAffected())
	}

	// An invitation nobody redeemed is not a link, and a pending row with a
	// dead code in it is a row an administrator has to work out the meaning of.
	// The row stays — it is a record that somebody was invited — but the code
	// stops being one.
	if _, err := s.db.Exec(sweepCtx, `
		UPDATE urtuu_peers
		   SET invite_code_hash = NULL, updated_at = NOW()
		 WHERE invite_code_hash IS NOT NULL AND invite_expires_at < NOW()`); err != nil {
		slog.Warn("urtuu: could not expire invitations", "error", err)
	}

	// A sanity line rather than a metric — the Prometheus gauges arrive with
	// the dashboard. An operator reading logs should still be able to see that
	// something is stuck.
	var stuck int
	if err := s.db.QueryRow(sweepCtx, `
		SELECT count(*) FROM urtuu_deliveries
		 WHERE delivered_at IS NULL AND created_at < $1`,
		time.Now().Add(-contract.MaxAge)).Scan(&stuck); err == nil && stuck > 0 {
		slog.Warn("urtuu: envelopes have been undelivered for longer than they may be accepted",
			"count", stuck, "max_age", contract.MaxAge)
	}
}
