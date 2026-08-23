/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package egov

import (
	"context"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/egov"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// history is domain/egov.History, answered from the platform's audit trail.
//
// It lived in domain/egov/postgres and ran `SELECT ... FROM audit_events`
// itself. Two things were wrong with that, and the second is the one a test
// caught. audit_events is the platform's table, so a module reading it directly
// depends on the platform's schema in a way no compiler sees — one that
// survives the module moving to another repository, right up until a column is
// renamed. And the domain layer may not name the SDK at all (ADR 0001,
// TestTheDomainDoesNotNameTheSDK): a rule that a store reaching for
// nexus.AuditReader breaks the moment it is written in domain/.
//
// So the port stays in the domain and the adapter lives here, in the module,
// which is the only layer allowed to know both.
type history struct{ audit nexus.AuditReader }

// Lookups is the last two hundred questions this organisation asked the state.
//
// `xyp` is in the prefix list on purpose: events written before the rename are
// the same acts under their old name, and a history that started empty on the
// day this module shipped would look like a history that had been cleared.
func (h history) Lookups(ctx context.Context, tenantID string) ([]domain.Lookup, error) {
	if h.audit == nil {
		return []domain.Lookup{}, nil
	}
	entries, err := h.audit.RecentByPrefix(ctx, tenantID, []string{"egov", "xyp"}, 200)
	if err != nil {
		return nil, err
	}
	lookups := make([]domain.Lookup, 0, len(entries))
	for _, entry := range entries {
		lookups = append(lookups, domain.Lookup{
			Action:    entry.Action,
			UserID:    entry.UserID,
			Details:   entry.Details,
			CreatedAt: entry.At,
		})
	}
	return lookups, nil
}
