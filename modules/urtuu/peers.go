/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package urtuu

import (
	"context"
	"log/slog"
)

// Who is on the other end of a link, for the one thing a task board needs to
// know about them: what to call them.
//
// This was `LEFT JOIN urtuu_peers` on every task, every event, every envelope
// and three reports. That table belongs to the channel — internal/platform/urtuu
// — and a module reading it is a dependency on the platform's schema that no
// compiler sees. ADR 0004 named exactly this as what kept this app in the core.
//
// The shape is the one people.go already uses for the directory: one read per
// page, and the ids are turned into names in memory. A board of five hundred
// tasks was five hundred joins for an answer that repeats twice.
func (m *Module) peerNames(ctx context.Context, tenantID string) map[string]string {
	peers, err := m.peers.Peers(ctx, tenantID)
	if err != nil {
		slog.Warn("urtuu: the links could not be read; peer names will be blank",
			"tenant_id", tenantID, "error", err)
		return nil
	}
	byID := make(map[string]string, len(peers))
	for _, peer := range peers {
		byID[peer.ID] = peer.Name
	}
	return byID
}

// namePeers turns the peer ids on a page of tasks into names.
//
// Nothing is read when no task on the page came from or went to a peer, which
// is the ordinary case for an installation that has never established a link
// and the common case for one that has.
func (m *Module) namePeers(ctx context.Context, tenantID string, tasks []Task) {
	linked := false
	for i := range tasks {
		if tasks[i].OriginPeerID != "" || tasks[i].TargetPeerID != "" {
			linked = true
			break
		}
	}
	if !linked {
		return
	}
	names := m.peerNames(ctx, tenantID)
	for i := range tasks {
		if tasks[i].OriginPeerID != "" {
			tasks[i].OriginPeerName = names[tasks[i].OriginPeerID]
		}
		if tasks[i].TargetPeerID != "" {
			tasks[i].TargetPeerName = names[tasks[i].TargetPeerID]
		}
	}
}
