/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package urtuu

import (
	"context"
	"errors"
	"sync"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The platform's record of who belongs where, as this module sees it.
//
// It was internal/platform/directory over a migrated Postgres while this app
// lived in the core. That package could not come here and should not have: the
// implementation is the platform's to test, and what these tests are about is
// what this app does with the answer.
type fakePeople struct {
	mu     sync.Mutex
	people []nexus.DirectoryPerson
}

func (d *fakePeople) add(person nexus.DirectoryPerson) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.people = append(d.people, person)
}

// deactivate is an organisation saying somebody has left. Their account still
// signs in — they may belong to another organisation — and this one no longer
// gives them work.
func (d *fakePeople) deactivate(userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.people {
		if d.people[i].UserID == userID {
			d.people[i].Active = false
		}
	}
}

// Membership, CountAdmins and SetActive complete nexus.Directory. This app
// calls none of them — it asks People and nothing else — and a fake that
// implemented them would be describing behaviour nothing here depends on.
func (d *fakePeople) Membership(context.Context, string, string) (nexus.DirectoryMembership, error) {
	return nexus.DirectoryMembership{}, errors.New("the task board does not read memberships")
}

func (d *fakePeople) CountAdmins(context.Context, string, string) (int, error) {
	return 0, errors.New("the task board does not count administrators")
}

func (d *fakePeople) SetActive(context.Context, string, string, bool) (bool, error) {
	return false, errors.New("the task board does not deactivate anybody")
}

func (d *fakePeople) People(_ context.Context, tenantIDs []string) ([]nexus.DirectoryPerson, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	wanted := make(map[string]bool, len(tenantIDs))
	for _, id := range tenantIDs {
		wanted[id] = true
	}
	found := make([]nexus.DirectoryPerson, 0, len(d.people))
	for _, person := range d.people {
		if wanted[person.TenantID] {
			found = append(found, person)
		}
	}
	return found, nil
}
