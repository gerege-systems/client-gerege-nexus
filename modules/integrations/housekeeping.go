package integrations

import (
	"context"
	"log/slog"
	"time"
)

// Two tables here only ever grow on their own.
//
// integration_oauth_states gains a row every time somebody presses Connect and
// loses one only when they come back from the consent screen — so every
// abandoned attempt, every closed tab, stays forever. SweepOAuthStates existed
// for this and was never called by anything, which is the same as not existing.
//
// integration_deliveries gains a row for every event, export and meeting.
const (
	housekeepingInterval = time.Hour

	// deliveryRetention is how long the record of what left the platform is
	// kept. A signed document reaching an outside account is a disclosure, and
	// the question "where did this go" is asked months later, not hours later.
	deliveryRetention = 180 * 24 * time.Hour
)

// StartHousekeeping runs the periodic cleanup until ctx is cancelled.
//
// It returns immediately, and it sweeps once on the way in: a deployment that
// restarts more often than the interval would otherwise never sweep at all.
func (m *Manager) StartHousekeeping(ctx context.Context) {
	background("integration-housekeeping", func() {
		m.sweepOnce(ctx)

		ticker := time.NewTicker(housekeepingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sweepOnce(ctx)
			}
		}
	})
}

// sweepOnce is bounded so a slow database cannot pile one sweep on top of the
// next when the interval comes round again.
func (m *Manager) sweepOnce(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if states, err := m.SweepOAuthStates(sweepCtx); err != nil {
		slog.Warn("integration: could not sweep abandoned connect attempts", "error", err)
	} else if states > 0 {
		slog.Info("integration: removed abandoned connect attempts", "count", states)
	}

	cutoff := time.Now().Add(-deliveryRetention)
	if pruned, err := m.store.deleteDeliveriesBefore(sweepCtx, cutoff); err != nil {
		slog.Warn("integration: could not prune the delivery log", "error", err)
	} else if pruned > 0 {
		slog.Info("integration: pruned the delivery log", "count", pruned, "older_than", cutoff)
	}
}
