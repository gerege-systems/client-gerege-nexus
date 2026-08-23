/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package organisation is the organisation, its structure and its people —
 * without the platform it happens to run on.
 *
 * Nothing here knows about HTTP, chi, pgx or pkg/nexus. What it knows is that
 * the last administrator stays, that a unit cannot report to its own
 * descendant, and that an archived parent holds its children back. Those are
 * the sentences somebody would say out loud about this app, and until now every
 * one of them was a paragraph inside an HTTP handler, reachable only by making
 * a request against a migrated database.
 *
 * The adapter that mounts this on the platform is internal/apps/organisation.
 * The import can only go that way: an app that can be read without the platform
 * is an app that can be moved, and a domain that names the SDK is already the
 * platform's shape wearing a different directory.
 */
package organisation

import "strings"

// Person is somebody as this organisation knows them.
//
// The identity is the user's and is shared across organisations; the job title,
// the department and whether they still work here belong to the membership,
// because all three can differ in the next tenant the same person belongs to.
//
// The JSON tags are here rather than in the adapter on purpose: they are the
// published shape of this app's answers, and a second set of structs at the
// edge would be a second place for that shape to drift.
type Person struct {
	MembershipID   string   `json:"membership_id"`
	UserID         string   `json:"user_id"`
	Name           string   `json:"name"`
	Email          string   `json:"email"`
	Phone          string   `json:"phone"`
	JobTitle       string   `json:"job_title"`
	DepartmentID   string   `json:"department_id,omitempty"`
	DepartmentName string   `json:"department_name,omitempty"`
	Active         bool     `json:"active"`
	IsAdmin        bool     `json:"is_admin"`
	Roles          []string `json:"roles"`
	JoinedAt       string   `json:"joined_at"`
	// Which organisation this membership is in. Only meaningful when the
	// session is reading across more than one, which is when the screen shows
	// it — a column repeating the same name on every row is noise.
	TenantID   string `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
}

// Department is one node of the organisation's structure.
type Department struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	ParentID    string `json:"parent_id,omitempty"`
	ManagerID   string `json:"manager_membership_id,omitempty"`
	ManagerName string `json:"manager_name,omitempty"`
	Active      bool   `json:"active"`
	PeopleCount int    `json:"people_count"`
	TenantID    string `json:"tenant_id"`
	TenantName  string `json:"tenant_name"`
}

// Membership is the little the deactivation rules need to know about somebody:
// who they are, so the caller cannot be themselves, and whether they administer
// this organisation, so the last one cannot be the one going.
type Membership struct {
	UserID  string
	IsAdmin bool
}

// PersonEdit is what an administrator may change about a membership.
//
// The person's name, email and language are theirs — editable from their own
// preferences and nowhere else — because an administrator of one tenant
// renaming a user would rename them in every other tenant that person belongs
// to.
type PersonEdit struct {
	JobTitle     *string `json:"job_title"`
	DepartmentID *string `json:"department_id"`
}

// Department reports what to write and whether to write it at all.
//
// An empty department means "none", which is a value rather than an omission —
// hence the two answers: absent leaves it alone, empty clears it. Both stores
// ask this rather than deciding it, so the two cannot disagree.
func (e PersonEdit) Department() (value *string, set bool) {
	if e.DepartmentID == nil {
		return nil, false
	}
	return trimmedOrNil(e.DepartmentID), true
}

// DepartmentEdit is a unit as somebody has just described it.
type DepartmentEdit struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	ParentID  *string `json:"parent_id"`
	ManagerID *string `json:"manager_membership_id"`
}

// Parent is the unit this one reports to, or nil for a root.
func (e DepartmentEdit) Parent() *string { return trimmedOrNil(e.ParentID) }

// Manager is the membership that heads this unit, or nil for none.
func (e DepartmentEdit) Manager() *string { return trimmedOrNil(e.ManagerID) }

// trimmedOrNil turns an absent or blank identifier into nothing, so "no parent"
// and "unchanged" do not both arrive as the empty string.
func trimmedOrNil(value *string) *string {
	if value == nil {
		return nil
	}
	if trimmed := strings.TrimSpace(*value); trimmed != "" {
		return &trimmed
	}
	return nil
}
