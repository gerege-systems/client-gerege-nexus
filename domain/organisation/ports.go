/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package organisation

import "context"

// Repository is everything this app asks of storage, and no more.
//
// It is written as questions and statements rather than as queries: "how many
// administrators are left", not "run this SELECT". That is the line that
// decides where a rule lives — a port that answered "is this deletable" would
// have taken the rule with it into SQL, where it cannot be read, cannot be
// tested without a database, and would have to be written again in the next
// store.
//
// Every method is tenant-scoped by argument as well as by row-level security.
// The argument is not the protection — the policies are — but a query that does
// not name the tenant reads as though nobody thought about it.
type Repository interface {
	// ListPeople reads across every organisation the session may see, which is
	// usually the one it is acting in.
	ListPeople(ctx context.Context, tenantIDs []string) ([]Person, error)
	// Membership answers ErrCrossTenant when the membership is not this
	// tenant's, which includes not existing at all.
	Membership(ctx context.Context, tenantID, membershipID string) (Membership, error)
	// UpdatePerson reports whether the membership was there to be updated.
	UpdatePerson(ctx context.Context, tenantID, membershipID string, edit PersonEdit) (bool, error)
	// CountAdmins counts the active administrators of this organisation other
	// than the membership named, which is the one about to leave.
	CountAdmins(ctx context.Context, tenantID, exceptMembershipID string) (int, error)
	SetPersonActive(ctx context.Context, tenantID, membershipID string, active bool) (bool, error)

	ListDepartments(ctx context.Context, tenantIDs []string) ([]Department, error)
	CreateDepartment(ctx context.Context, tenantID string, edit DepartmentEdit) (string, error)
	UpdateDepartment(ctx context.Context, tenantID, id string, edit DepartmentEdit) (bool, error)
	// IsDescendant reports whether candidate stands anywhere below ancestor. It
	// is a walk rather than a row, which is why no database constraint can hold
	// this rule and the service has to ask.
	IsDescendant(ctx context.Context, tenantID, ancestorID, candidateID string) (bool, error)
	// Parent describes the unit this one reports to. A unit with no parent —
	// and a unit that is not there at all — answers with an empty name and
	// false, leaving the refusal to whatever runs next.
	Parent(ctx context.Context, tenantID, id string) (name string, archived bool, err error)
	SetDepartmentArchived(ctx context.Context, tenantID, id string, archived bool) (bool, error)
	// CountChildren counts what points at a unit: the people working in it and
	// the units under it.
	CountChildren(ctx context.Context, tenantID, id string) (people, units int, err error)
	DeleteDepartment(ctx context.Context, tenantID, id string) (bool, error)
}
