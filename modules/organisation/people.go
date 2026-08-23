/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package organisation

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/organisation"
	"github.com/gerege-systems/client-gerege-nexus/domain/organisation/postgres"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// repository is the domain's port, answered from two places.
//
// Departments and the two facts this app keeps about a person — job title and
// which department — are its own, in its own tables. Who the person is, which
// organisation they belong to and which roles they hold are the platform's, and
// reached through nexus.Directory.
//
// The join happens here because the module is the only layer allowed to know
// both: the domain may not name the SDK (ADR 0001), and the platform may not
// name the app. It used to happen in one SQL statement across six tables, four
// of which were the platform's — which is what made this app unmovable.
type repository struct {
	*postgres.Store
	people nexus.Directory
}

// ErrNoDirectory is what every directory-backed method answers on a deployment
// that provides none. Not a nil dereference and not an empty list: an empty
// staff list reads as "nobody works here", which is a worse answer than "this
// cannot be read".
var ErrNoDirectory = errors.New("this deployment provides no directory of people")

func (r repository) ListPeople(ctx context.Context, tenantIDs []string) ([]domain.Person, error) {
	if r.people == nil {
		return nil, ErrNoDirectory
	}
	members, err := r.people.People(ctx, tenantIDs)
	if err != nil {
		return nil, err
	}
	details, err := r.Details(ctx, tenantIDs)
	if err != nil {
		return nil, err
	}

	people := make([]domain.Person, 0, len(members))
	for _, member := range members {
		detail := details[member.MembershipID]
		people = append(people, domain.Person{
			MembershipID: member.MembershipID, UserID: member.UserID,
			Name: member.Name, Email: member.Email, Phone: member.Phone,
			JobTitle:     detail.JobTitle,
			DepartmentID: detail.DepartmentID, DepartmentName: detail.DepartmentName,
			Active: member.Active, IsAdmin: member.IsAdmin,
			Roles: member.Roles, JoinedAt: member.JoinedAt,
			TenantID: member.TenantID, TenantName: member.TenantName,
		})
	}
	return people, nil
}

// ListDepartments fills in the manager's name, which the store cannot know.
//
// The department row holds a membership id; who that membership belongs to is
// the platform's answer. One directory read per call rather than per row: a
// staff list of two hundred would otherwise be two hundred queries.
func (r repository) ListDepartments(ctx context.Context, tenantIDs []string) ([]domain.Department, error) {
	departments, err := r.Store.ListDepartments(ctx, tenantIDs)
	if err != nil || r.people == nil {
		return departments, err
	}
	// The departments are readable and the names may not be. Answering with the
	// departments is better than refusing the screen: a unit with a blank
	// manager is legible, and a 500 is not. The error is logged rather than
	// dropped — a directory that has stopped answering is worth knowing about
	// even when the screen survives it.
	members, err := r.people.People(ctx, tenantIDs)
	if err != nil {
		slog.Warn("organisation: the directory could not be read; unit managers will be blank", "error", err)
		return departments, nil //nolint:nilerr // deliberate: see above
	}
	names := make(map[string]string, len(members))
	organisations := map[string]string{}
	for _, member := range members {
		names[member.MembershipID] = member.Name
		organisations[member.TenantID] = member.TenantName
	}
	for i := range departments {
		departments[i].ManagerName = names[departments[i].ManagerID]
		// The organisation's own name, which the store stopped joining for:
		// `tenants` is the platform's table and the directory already carries
		// the answer for every membership it returned.
		departments[i].TenantName = organisations[departments[i].TenantID]
	}
	// Sorted here rather than in SQL, now that the name the sort is on is not
	// in the query any more.
	sort.SliceStable(departments, func(a, b int) bool {
		if departments[a].TenantName != departments[b].TenantName {
			return departments[a].TenantName < departments[b].TenantName
		}
		if departments[a].Active != departments[b].Active {
			return departments[a].Active
		}
		return departments[a].Name < departments[b].Name
	})
	return departments, nil
}

func (r repository) Membership(ctx context.Context, tenantID, membershipID string) (domain.Membership, error) {
	if r.people == nil {
		return domain.Membership{}, ErrNoDirectory
	}
	found, err := r.people.Membership(ctx, tenantID, membershipID)
	if err != nil {
		return domain.Membership{}, err
	}
	return domain.Membership{UserID: found.UserID, IsAdmin: found.IsAdmin}, nil
}

func (r repository) CountAdmins(ctx context.Context, tenantID, exceptMembershipID string) (int, error) {
	if r.people == nil {
		return 0, ErrNoDirectory
	}
	return r.people.CountAdmins(ctx, tenantID, exceptMembershipID)
}

func (r repository) SetPersonActive(ctx context.Context, tenantID, membershipID string, active bool) (bool, error) {
	if r.people == nil {
		return false, ErrNoDirectory
	}
	return r.people.SetActive(ctx, tenantID, membershipID, active)
}

// The domain's port, answered by the two halves together. The assertion is here
// rather than beside the store because the store alone no longer satisfies it —
// which is the change, stated so a compiler keeps it true.
var _ domain.Repository = repository{}
