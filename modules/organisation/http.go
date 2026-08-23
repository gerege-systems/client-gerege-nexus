/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The organisation, mounted on the platform.
 *
 * Every handler here does three things and nothing else: decode the request,
 * ask the domain, turn the domain's answer into a status. The rules moved to
 * backend/domain/organisation, which knows nothing about HTTP and can be read
 * and tested without a database; what is left is the translation, and it is
 * short enough to check by eye against the routes it serves.
 *
 * The statuses are the ones this app has always sent. They are in one map now
 * — status() — rather than spread across ten handlers, which is the only part
 * of this refactor a client could notice if it were got wrong.
 */

package organisation

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	org "github.com/gerege-systems/client-gerege-nexus/domain/organisation"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// Person and Department are the domain's types under this package's names.
//
// The aliases are not politeness: `organisation.Person` is what the module's
// own tests and any distribution reading this app's answers already name, and a
// second struct here would be a second place for the published JSON shape to
// drift from the one the domain sends.
type (
	Person     = org.Person
	Department = org.Department
)

func (m *Module) handleListPeople(w http.ResponseWriter, r *http.Request) {
	if _, ok := nexus.RequireTenant(w, r); !ok {
		return
	}
	// Across every organisation this session is active in, which is just the
	// one it is acting in unless somebody has asked for more. The row-level
	// policies allow exactly the same set, so this widens what is asked for
	// without widening what could be reached.
	people, err := m.svc.People(r.Context(), nexus.AllowedTenants(r.Context()))
	if err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, people)
}

func (m *Module) handleUpdatePerson(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	var edit org.PersonEdit
	if !decode(w, r, &edit) {
		return
	}
	if err := m.svc.UpdatePerson(r.Context(), tenantID, chi.URLParam(r, "id"), edit); err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (m *Module) handleDeactivatePerson(w http.ResponseWriter, r *http.Request) {
	m.setPersonActive(w, r, false)
}

func (m *Module) handleReactivatePerson(w http.ResponseWriter, r *http.Request) {
	m.setPersonActive(w, r, true)
}

func (m *Module) setPersonActive(w http.ResponseWriter, r *http.Request, active bool) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	// Who is asking matters here: one of the two refusals is about the caller
	// deactivating themselves, and only the platform can say who that is.
	claims, err := nexus.UserFromContext(r.Context())
	if err != nil {
		nexus.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := m.svc.SetPersonActive(r.Context(), tenantID, chi.URLParam(r, "id"), claims.UserID, active); err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]bool{"active": active})
}

// --- departments ------------------------------------------------------------

func (m *Module) handleListDepartments(w http.ResponseWriter, r *http.Request) {
	if _, ok := nexus.RequireTenant(w, r); !ok {
		return
	}
	list, err := m.svc.Departments(r.Context(), nexus.AllowedTenants(r.Context()))
	if err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (m *Module) handleCreateDepartment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	var edit org.DepartmentEdit
	if !decode(w, r, &edit) {
		return
	}
	id, err := m.svc.CreateDepartment(r.Context(), tenantID, edit)
	if err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (m *Module) handleUpdateDepartment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	var edit org.DepartmentEdit
	if !decode(w, r, &edit) {
		return
	}
	if err := m.svc.UpdateDepartment(r.Context(), tenantID, chi.URLParam(r, "id"), edit); err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (m *Module) handleArchiveDepartment(w http.ResponseWriter, r *http.Request) {
	m.setDepartmentArchived(w, r, true)
}

func (m *Module) handleRestoreDepartment(w http.ResponseWriter, r *http.Request) {
	m.setDepartmentArchived(w, r, false)
}

func (m *Module) setDepartmentArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	if err := m.svc.SetDepartmentArchived(r.Context(), tenantID, chi.URLParam(r, "id"), archived); err != nil {
		fail(w, r, err)
		return
	}
	status := "restored"
	if archived {
		status = "archived"
	}
	nexus.JSON(w, http.StatusOK, map[string]string{"status": status})
}

func (m *Module) handleDeleteDepartment(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	if err := m.svc.DeleteDepartment(r.Context(), tenantID, chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- translation -------------------------------------------------------------

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(into); err != nil {
		nexus.Error(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

// fail answers with the domain's own words and the status that rule has always
// had. Anything that is not a rule is a failure of storage, which is a 500 and
// a log line — the operator needs the cause, and the caller needs only to know
// it was not their fault.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	code := status(err)
	if code >= http.StatusInternalServerError {
		slog.Error("organisation: "+err.Error(), "error", errors.Unwrap(err), "path", r.URL.Path)
	}
	nexus.Error(w, code, err.Error())
}

// status is the whole of what this app says in HTTP that it does not say in Go.
//
// Not found is 404 rather than 403 on purpose: whether a membership or a unit
// exists in another organisation is not this caller's business. The refusals
// that describe a conflict with something that already exists are 409, and the
// ones that describe a request that was wrong to make are 400.
func status(err error) int {
	switch {
	case errors.Is(err, org.ErrCrossTenant), errors.Is(err, org.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, org.ErrDuplicateCode), errors.Is(err, org.ErrCycle),
		errors.Is(err, org.ErrParentArchived), errors.Is(err, org.ErrUnitNotEmpty):
		return http.StatusConflict
	case errors.Is(err, org.ErrSelfDeactivation), errors.Is(err, org.ErrLastAdministrator),
		errors.Is(err, org.ErrSelfParent), errors.Is(err, org.ErrNameRequired),
		errors.Is(err, org.ErrInvalidCode), errors.Is(err, org.ErrForeignDepartment),
		errors.Is(err, org.ErrForeignUnit):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
