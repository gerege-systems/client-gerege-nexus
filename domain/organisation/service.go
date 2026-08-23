/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package organisation

import (
	"context"
	"strings"
)

// Service is the organisation's rules. It holds no state of its own: everything
// it knows it asks the repository for, and everything it decides it decides in
// Go, where the decision can be read and tested.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

// People is the directory: everybody in every organisation this session is
// active in.
func (s *Service) People(ctx context.Context, tenantIDs []string) ([]Person, error) {
	people, err := s.repo.ListPeople(ctx, tenantIDs)
	if err != nil {
		return nil, Failed("could not load the people", err)
	}
	return people, nil
}

// UpdatePerson edits what this organisation knows about somebody.
func (s *Service) UpdatePerson(ctx context.Context, tenantID, membershipID string, edit PersonEdit) error {
	found, err := s.repo.UpdatePerson(ctx, tenantID, membershipID, edit)
	if err != nil {
		return Failed("could not save the change", err)
	}
	if !found {
		return ErrCrossTenant
	}
	return nil
}

// SetPersonActive ends or resumes a membership without erasing it.
//
// Deactivation rather than deletion, because a membership is referenced by
// everything the person did here — a signature, an approval, a request they
// processed. Removing the row would either take that history with it or leave
// it pointing at nothing.
//
// Two refusals guard it, and both are about locking somebody out of an
// organisation nobody can then get back into. They apply to going, not to
// coming back: reactivating anybody is always safe.
func (s *Service) SetPersonActive(ctx context.Context, tenantID, membershipID, actorUserID string, active bool) error {
	if !active {
		person, err := s.repo.Membership(ctx, tenantID, membershipID)
		if err != nil {
			return Failed("could not read the membership", err)
		}
		// Locking yourself out is a support ticket, so it is refused here
		// rather than regretted later.
		if person.UserID == actorUserID {
			return ErrSelfDeactivation
		}
		// And neither is leaving an organisation with nobody who can administer
		// it: the last active administrator stays.
		remaining, err := s.repo.CountAdmins(ctx, tenantID, membershipID)
		if err != nil {
			return Failed("could not check the administrators", err)
		}
		if person.IsAdmin && remaining == 0 {
			return ErrLastAdministrator
		}
	}

	found, err := s.repo.SetPersonActive(ctx, tenantID, membershipID, active)
	if err != nil {
		return Failed("could not save the change", err)
	}
	if !found {
		return ErrCrossTenant
	}
	return nil
}

// Departments is the structure, as a flat list the caller can draw as a tree.
func (s *Service) Departments(ctx context.Context, tenantIDs []string) ([]Department, error) {
	list, err := s.repo.ListDepartments(ctx, tenantIDs)
	if err != nil {
		return nil, Failed("could not load the departments", err)
	}
	return list, nil
}

func (s *Service) CreateDepartment(ctx context.Context, tenantID string, edit DepartmentEdit) (string, error) {
	edit.Code = strings.TrimSpace(edit.Code)
	edit.Name = strings.TrimSpace(edit.Name)
	if edit.Name == "" || !isValidCode(edit.Code) {
		return "", ErrInvalidCode
	}

	id, err := s.repo.CreateDepartment(ctx, tenantID, edit)
	if err != nil {
		return "", Failed("could not create the department", err)
	}
	return id, nil
}

// UpdateDepartment renames a unit and moves it.
//
// The code is not editable and is not read here: it is what other things refer
// to the unit by.
func (s *Service) UpdateDepartment(ctx context.Context, tenantID, id string, edit DepartmentEdit) error {
	edit.Name = strings.TrimSpace(edit.Name)
	if edit.Name == "" {
		return ErrNameRequired
	}

	// A department cannot be its own parent, and cannot be moved under one of
	// its own descendants either. The schema refuses the first; the second is a
	// walk and no CHECK can see it. Leaving it to the screen was not enough —
	// the screen offers a tree it drew from the same data, and anything that
	// calls this directly could tie a knot that makes every reader which
	// follows parent_id loop for ever.
	if parent := edit.Parent(); parent != nil {
		if *parent == id {
			return ErrSelfParent
		}
		descendant, err := s.repo.IsDescendant(ctx, tenantID, id, *parent)
		if err != nil {
			return Failed("could not save the department", err)
		}
		if descendant {
			return ErrCycle
		}
	}

	found, err := s.repo.UpdateDepartment(ctx, tenantID, id, edit)
	if err != nil {
		return Failed("could not save the department", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// SetDepartmentArchived retires a unit without deleting it, or brings it back.
//
// People and history point at it. Archiving keeps those references readable and
// takes it out of every list that offers a choice — which is what somebody
// pressing "delete" on an empty-looking department actually wants.
//
// Coming back has one refusal: a unit cannot stand under a parent that is still
// archived, or the tree would draw it as a root, its own parent would be
// missing from every list that offers one, and the next edit would silently
// reparent it.
func (s *Service) SetDepartmentArchived(ctx context.Context, tenantID, id string, archived bool) error {
	if !archived {
		name, parentArchived, err := s.repo.Parent(ctx, tenantID, id)
		if err != nil {
			return Failed("could not read the department", err)
		}
		if parentArchived {
			return ArchivedParent{Name: name}
		}
	}

	message := "could not restore the department"
	if archived {
		message = "could not archive the department"
	}
	found, err := s.repo.SetDepartmentArchived(ctx, tenantID, id, archived)
	if err != nil {
		return Failed(message, err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// DeleteDepartment removes a unit that never really existed.
//
// Archiving is for a unit that did: people worked in it, documents name it, and
// the row has to stay for those to keep meaning anything. This is for the other
// case — a typo, a duplicate, a structure sketched out and thought better of —
// and it is refused the moment anything at all points at the row.
func (s *Service) DeleteDepartment(ctx context.Context, tenantID, id string) error {
	people, units, err := s.repo.CountChildren(ctx, tenantID, id)
	if err != nil {
		return Failed("could not check the department", err)
	}
	if people > 0 || units > 0 {
		return NotEmpty{People: people, Children: units}
	}

	found, err := s.repo.DeleteDepartment(ctx, tenantID, id)
	if err != nil {
		return Failed("could not delete the department", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// isValidCode is catalog.IsValidSlug, copied rather than called.
//
// Not a preference: pkg/catalog imports pkg/nexus, so calling it would drag the
// platform SDK into a package whose whole point is not to have it, and the
// dependency check in CI would say so. Ten lines of character test is the
// cheaper half of that trade.
//
// The two are held together by TestTheDomainAgreesWithTheCatalogueAboutSlugs in
// the adapter, which can import both and compares them; if this drifts, that
// fails rather than an app becoming installable and its departments not.
//
// Lowercase alphanumerics, hyphens and underscores, at most 64. Underscores are
// permitted because catalogue slugs used them.
func isValidCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for _, ch := range code {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}
