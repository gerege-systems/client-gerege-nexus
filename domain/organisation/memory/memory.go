/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * NOT FOR PRODUCTION. This store exists so that the organisation's rules can be
 * run without PostgreSQL: the tests next door assert that the last
 * administrator stays and that a unit cannot report to its own descendant, and
 * those are decisions Go makes, not the database.
 *
 * Nothing else may import it. It keeps everything in one process's memory, it
 * does not enforce a single one of the schema's constraints beyond what the
 * rules above it ask about, and a deployment that ran on it would lose the
 * organisation when the process restarted.
 */
package memory

import (
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/gerege-systems/client-gerege-nexus/domain/organisation"
)

type Store struct {
	mu          sync.Mutex
	people      []organisation.Person
	departments []organisation.Department
	nextID      int
}

func New() *Store { return &Store{} }

// AddPerson seeds a membership. Tests are the only caller: a real store is
// filled by the platform's invitation flow, which is not this app's.
func (s *Store) AddPerson(p organisation.Person) organisation.Person {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.MembershipID == "" {
		p.MembershipID = s.id()
	}
	if p.UserID == "" {
		p.UserID = "user-" + p.MembershipID
	}
	s.people = append(s.people, p)
	return p
}

func (s *Store) id() string {
	s.nextID++
	return strconv.Itoa(s.nextID)
}

func (s *Store) ListPeople(_ context.Context, tenantIDs []string) ([]organisation.Person, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	people := make([]organisation.Person, 0)
	for _, p := range s.people {
		if slices.Contains(tenantIDs, p.TenantID) {
			people = append(people, p)
		}
	}
	return people, nil
}

func (s *Store) Membership(_ context.Context, tenantID, membershipID string) (organisation.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findPerson(tenantID, membershipID)
	if i < 0 {
		return organisation.Membership{}, organisation.ErrCrossTenant
	}
	return organisation.Membership{UserID: s.people[i].UserID, IsAdmin: s.people[i].IsAdmin}, nil
}

func (s *Store) UpdatePerson(_ context.Context, tenantID, membershipID string, edit organisation.PersonEdit) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findPerson(tenantID, membershipID)
	if i < 0 {
		return false, nil
	}
	if edit.JobTitle != nil {
		s.people[i].JobTitle = *edit.JobTitle
	}
	if department, set := edit.Department(); set {
		if department == nil {
			s.people[i].DepartmentID = ""
		} else {
			// What the composite foreign key does in PostgreSQL.
			if s.findDepartment(tenantID, *department) < 0 {
				return false, organisation.ErrForeignDepartment
			}
			s.people[i].DepartmentID = *department
		}
	}
	return true, nil
}

func (s *Store) CountAdmins(_ context.Context, tenantID, exceptMembershipID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, p := range s.people {
		if p.TenantID == tenantID && p.Active && p.IsAdmin && p.MembershipID != exceptMembershipID {
			count++
		}
	}
	return count, nil
}

func (s *Store) SetPersonActive(_ context.Context, tenantID, membershipID string, active bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findPerson(tenantID, membershipID)
	if i < 0 {
		return false, nil
	}
	s.people[i].Active = active
	return true, nil
}

func (s *Store) ListDepartments(_ context.Context, tenantIDs []string) ([]organisation.Department, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]organisation.Department, 0)
	for _, d := range s.departments {
		if slices.Contains(tenantIDs, d.TenantID) {
			list = append(list, d)
		}
	}
	return list, nil
}

func (s *Store) CreateDepartment(_ context.Context, tenantID string, edit organisation.DepartmentEdit) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.departments {
		if d.TenantID == tenantID && d.Code == edit.Code {
			return "", organisation.ErrDuplicateCode
		}
	}
	if err := s.foreign(tenantID, edit); err != nil {
		return "", err
	}
	department := organisation.Department{
		ID: s.id(), Code: edit.Code, Name: edit.Name, Active: true, TenantID: tenantID,
	}
	if parent := edit.Parent(); parent != nil {
		department.ParentID = *parent
	}
	if manager := edit.Manager(); manager != nil {
		department.ManagerID = *manager
	}
	s.departments = append(s.departments, department)
	return department.ID, nil
}

func (s *Store) UpdateDepartment(_ context.Context, tenantID, id string, edit organisation.DepartmentEdit) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findDepartment(tenantID, id)
	if i < 0 {
		return false, nil
	}
	if err := s.foreign(tenantID, edit); err != nil {
		return false, err
	}
	s.departments[i].Name = edit.Name
	s.departments[i].ParentID = ""
	if parent := edit.Parent(); parent != nil {
		s.departments[i].ParentID = *parent
	}
	s.departments[i].ManagerID = ""
	if manager := edit.Manager(); manager != nil {
		s.departments[i].ManagerID = *manager
	}
	return true, nil
}

func (s *Store) IsDescendant(_ context.Context, tenantID, ancestorID, candidateID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Upwards from the candidate rather than down from the ancestor: the same
	// answer, and it cannot loop for ever on a tree that is already knotted —
	// each step is one parent, and the walk is bounded by the number of units.
	for id, steps := candidateID, 0; id != "" && steps <= len(s.departments); steps++ {
		i := s.findDepartment(tenantID, id)
		if i < 0 {
			return false, nil
		}
		if s.departments[i].ParentID == ancestorID {
			return true, nil
		}
		id = s.departments[i].ParentID
	}
	return false, nil
}

func (s *Store) Parent(_ context.Context, tenantID, id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findDepartment(tenantID, id)
	if i < 0 || s.departments[i].ParentID == "" {
		return "", false, nil
	}
	parent := s.findDepartment(tenantID, s.departments[i].ParentID)
	if parent < 0 {
		return "", false, nil
	}
	return s.departments[parent].Name, !s.departments[parent].Active, nil
}

func (s *Store) SetDepartmentArchived(_ context.Context, tenantID, id string, archived bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findDepartment(tenantID, id)
	if i < 0 {
		return false, nil
	}
	s.departments[i].Active = !archived
	return true, nil
}

func (s *Store) CountChildren(_ context.Context, tenantID, id string) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var people, units int
	for _, p := range s.people {
		if p.TenantID == tenantID && p.DepartmentID == id {
			people++
		}
	}
	for _, d := range s.departments {
		if d.TenantID == tenantID && d.ParentID == id {
			units++
		}
	}
	return people, units, nil
}

func (s *Store) DeleteDepartment(_ context.Context, tenantID, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findDepartment(tenantID, id)
	if i < 0 {
		return false, nil
	}
	s.departments = slices.Delete(s.departments, i, i+1)
	return true, nil
}

// foreign is the composite foreign key: a parent or a manager has to be in the
// same organisation as the unit naming it.
func (s *Store) foreign(tenantID string, edit organisation.DepartmentEdit) error {
	if parent := edit.Parent(); parent != nil && s.findDepartment(tenantID, *parent) < 0 {
		return organisation.ErrForeignUnit
	}
	if manager := edit.Manager(); manager != nil && s.findPerson(tenantID, *manager) < 0 {
		return organisation.ErrForeignUnit
	}
	return nil
}

func (s *Store) findPerson(tenantID, membershipID string) int {
	return slices.IndexFunc(s.people, func(p organisation.Person) bool {
		return p.MembershipID == membershipID && p.TenantID == tenantID
	})
}

func (s *Store) findDepartment(tenantID, id string) int {
	return slices.IndexFunc(s.departments, func(d organisation.Department) bool {
		return d.ID == id && d.TenantID == tenantID
	})
}

// Store is a Repository, checked here rather than at the one call site that
// would otherwise be the first to find out.
var _ organisation.Repository = (*Store)(nil)
