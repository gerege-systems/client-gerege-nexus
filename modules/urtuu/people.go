/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package urtuu

import (
	"context"
	"log/slog"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// Who this organisation's people are, for the two things a task board needs to
// know about them: whether somebody may be given work, and what to call them.
//
// Both were SQL against the platform's tables — `SELECT EXISTS (... FROM
// memberships ...)` before an assignment, and `LEFT JOIN users` on every task
// and every event to turn an id into a name. Neither is this app's data, and a
// module reading `users` is a dependency on the platform's schema that no
// compiler sees.
//
// One directory read answers both, and answers them for a whole page of tasks
// at once rather than joining per row.
func (m *Module) directory(ctx context.Context, tenantID string) map[string]nexus.DirectoryPerson {
	people, err := nexus.People()
	if err != nil {
		slog.Warn("urtuu: this deployment provides no directory; names will be blank", "error", err)
		return nil
	}
	members, err := people.People(ctx, []string{tenantID})
	if err != nil {
		slog.Warn("urtuu: the directory could not be read; names will be blank", "error", err)
		return nil
	}
	byUser := make(map[string]nexus.DirectoryPerson, len(members))
	for _, member := range members {
		byUser[member.UserID] = member
	}
	return byUser
}

// isMember reports whether this user may be given work here.
//
// Membership rather than existence: a user id from another organisation would
// otherwise be assignable, and no row-level policy covers `users` — people
// belong to several organisations by design.
//
// Refuses when the directory cannot be read. The alternative — assuming
// membership because the check failed — is how somebody outside an
// organisation ends up holding its work.
func (m *Module) isMember(ctx context.Context, tenantID, userID string) bool {
	member, found := m.directory(ctx, tenantID)[userID]
	return found && member.Active
}
