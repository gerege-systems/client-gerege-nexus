/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * What the channel exports to Prometheus.
 *
 * No tenant label on any of it — the rule this platform set in its first
 * monitoring phase and has kept (see internal/kernel/telemetry/business.go
 * for the argument). A label whose values are organisations is a series count
 * that only grows, and the per-organisation view is a reporting question
 * answered from the database, where a row can be deleted.
 *
 * These live here rather than in telemetry/business.go for the reason
 * written there about invoices: a metric named after a domain belongs to the
 * code that knows when the thing happened. A deployment that does not run
 * Өртөө exports nothing rather than a permanent zero.
 */

package channel

import (
	"context"
	"log/slog"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// deliveriesTotal counts envelope deliveries as they are settled.
	// result: ok — the other side acknowledged holding it; failed — an
	// exchange with that link did not complete and will be retried.
	deliveriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "urtuu_deliveries_total",
			Help: "Өртөө envelope deliveries by outcome",
		},
		[]string{"result"},
	)

	// peerLastSeenSeconds is how long the quietest live link has been quiet,
	// per direction.
	//
	// Per *direction* and not per peer, deliberately. A peer label would name
	// one organisation's counterparty in a metric, which is the tenant problem
	// wearing a different hat, and it would keep the series of every link that
	// was ever revoked. Two series answer the question an alert actually asks —
	// "has anything stopped talking to us" — and the links screen names which
	// one.
	peerLastSeenSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "urtuu_peer_last_seen_seconds",
			Help: "Seconds since the least recently seen live Өртөө link last spoke, by our role on it",
		},
		[]string{"role"},
	)
)

func init() {
	prometheus.MustRegister(deliveriesTotal, peerLastSeenSeconds)
}

// Delivery outcomes, as label values rather than strings typed at each site.
const (
	deliveryOK     = "ok"
	deliveryFailed = "failed"
)

// refreshPeerGauge re-reads how quiet the links have been.
//
// On the exchange loop rather than on the hourly sweep: a gauge an alert reads
// has to move at the pace of the thing it describes, and an hour of staleness
// is most of the window in which somebody would want to know.
//
// Links that have never been seen count from when they were created, so a link
// that was established and then never spoke shows as silent rather than as
// absent — which is exactly the failure this exists to surface.
func (s *Service) refreshPeerGauge(ctx context.Context) {
	rows, err := s.db.Query(nexus.WithoutWorkspace(ctx), `
		SELECT role, max(EXTRACT(EPOCH FROM (NOW() - coalesce(last_seen_at, created_at))))
		  FROM urtuu_peers
		 WHERE status = 'active' AND revoked_at IS NULL AND installation_id = $1
		 GROUP BY role`, s.installationID)
	if err != nil {
		slog.Warn("urtuu: could not read the links' health", "error", err)
		return
	}
	defer rows.Close()

	// Both roles reset first: a deployment whose last child link was revoked
	// would otherwise keep exporting the age it had at that moment for ever.
	seen := map[string]bool{"parent": false, "child": false}
	for rows.Next() {
		var role string
		var seconds float64
		if err := rows.Scan(&role, &seconds); err != nil {
			slog.Warn("urtuu: could not read a link's health", "error", err)
			return
		}
		peerLastSeenSeconds.WithLabelValues(role).Set(seconds)
		seen[role] = true
	}
	for role, found := range seen {
		if !found {
			peerLastSeenSeconds.WithLabelValues(role).Set(0)
		}
	}
}
