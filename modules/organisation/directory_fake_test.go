/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package organisation_test

import (
	"context"
	"errors"
	"sync"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The platform's record of who belongs where, as this module sees it.
//
// It was internal/platform/directory over a migrated Postgres, which is under
// internal/ and could not come here. That is the right outcome twice over: the
// implementation is the platform's to test, and what these tests are about is
// the module's own half — the join of a person with the job title and unit this
// app keeps about them, which is the only part of a staff list that is this
// app's answer.
type fakeDirectory struct {
	mu     sync.Mutex
	people []nexus.DirectoryPerson
}

func (d *fakeDirectory) add(person nexus.DirectoryPerson) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.people = append(d.people, person)
}

func (d *fakeDirectory) People(_ context.Context, tenantIDs []string) ([]nexus.DirectoryPerson, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	wanted := map[string]bool{}
	for _, id := range tenantIDs {
		wanted[id] = true
	}
	found := make([]nexus.DirectoryPerson, 0, len(d.people))
	for _, person := range d.people {
		if wanted[person.WorkspaceID] {
			found = append(found, person)
		}
	}
	return found, nil
}

var errNoSuchMembership = errors.New("no such membership in this organisation")

func (d *fakeDirectory) Membership(_ context.Context, tenantID, membershipID string) (nexus.DirectoryMembership, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, person := range d.people {
		if person.WorkspaceID == tenantID && person.MembershipID == membershipID {
			return nexus.DirectoryMembership{UserID: person.UserID, IsAdmin: person.IsAdmin}, nil
		}
	}
	return nexus.DirectoryMembership{}, errNoSuchMembership
}

func (d *fakeDirectory) CountAdmins(_ context.Context, tenantID, exceptMembershipID string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for _, person := range d.people {
		if person.WorkspaceID == tenantID && person.Active && person.IsAdmin && person.MembershipID != exceptMembershipID {
			count++
		}
	}
	return count, nil
}

func (d *fakeDirectory) SetActive(_ context.Context, tenantID, membershipID string, active bool) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.people {
		if d.people[i].WorkspaceID == tenantID && d.people[i].MembershipID == membershipID {
			d.people[i].Active = active
			return true, nil
		}
	}
	return false, nil
}

// everyPermission stands in for the platform's store. Who holds a permission is
// Access control's business and is tested there; the module's own gates are
// what this package is about.
type everyPermission struct{}

func (everyPermission) GetUserPermissions(context.Context, string, string) (map[string]bool, error) {
	return map[string]bool{"organisation.read": true, "organisation.manage": true}, nil
}
