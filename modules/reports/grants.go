/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Sharing a report with another organisation: request, accept, revoke, read.
 *
 * The rules are in backend/domain/reports — who may ask, who may agree, what a
 * scope means. What is here is the request, the audit entry and the status.
 */

package reports

import (
	"encoding/json"
	"net/http"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxGrantBody = 8 << 10

// handleListGrants returns every grant this organisation is a party to.
//
// Read through the platform's contract: the grants table is also what the
// consolidated run reads, and a second reader here would be a second shape for
// the same row.
func (m *Module) handleListGrants(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	grants, err := m.grants.List(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the sharing agreements")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"grants": grants})
}

// handleRequestGrant is the grantee asking to be shown a report.
func (m *Module) handleRequestGrant(w http.ResponseWriter, r *http.Request) {
	granteeTenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxGrantBody)
	var request domain.GrantRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid request")
		return
	}

	grant, err := m.svc.RequestGrant(acting(r), granteeTenantID, request)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, granteeTenantID, "reports.grant_requested", grant.ReportKey, map[string]any{
		"grant_id": grant.ID, "grantor_tenant_id": grant.GrantorTenantID, "scope": grant.Scope,
	})
	nexus.JSON(w, http.StatusCreated, map[string]any{"id": grant.ID})
}

// handleAcceptGrant is the grantor agreeing. Only they can.
func (m *Module) handleAcceptGrant(w http.ResponseWriter, r *http.Request) {
	grantorTenantID, id, ok := m.grantParty(w, r)
	if !ok {
		return
	}

	reportKey, err := m.svc.AcceptGrant(acting(r), grantorTenantID, id)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, grantorTenantID, "reports.grant_accepted", reportKey, map[string]any{"grant_id": id})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleRevokeGrant ends an agreement. Either side may.
func (m *Module) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	tenantID, id, ok := m.grantParty(w, r)
	if !ok {
		return
	}

	reportKey, side, err := m.svc.RevokeGrant(r.Context(), tenantID, id)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, tenantID, "reports.grant_revoked", reportKey, map[string]any{
		"grant_id": id, "side": side,
	})
	nexus.JSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleRunConsolidated runs a report across every organisation that shares it.
func (m *Module) handleRunConsolidated(w http.ResponseWriter, r *http.Request) {
	tenantID, report, ok := m.resolve(w, r)
	if !ok {
		return
	}
	// The consolidated view is not gated on the grantee having the *grantor's*
	// app: it is gated on the grant, and on this organisation having the
	// reports app and the permission. resolve has already checked that the
	// report's own app is installed here, which is what makes the report
	// meaningful to them at all.

	raw, ok := decodeParams(w, r)
	if !ok {
		return
	}

	run, err := m.engine.RunConsolidated(r.Context(), tenantID, report.Key, raw, localeOf(r), actorOf(r))
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	nexus.JSON(w, http.StatusOK, run)
}

// handleAccessHistory is the owner's answer to "who has read our data".
//
// It reads this organisation's own audit rows for the one action the
// consolidated engine writes on the owner's side. Ordinary audit is not exposed
// through any API; this slice of it is, because §3.5 requires the data owner to
// be able to see it and a trail they cannot read is not a control.
func (m *Module) handleAccessHistory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	// Read through the platform's contract, which reads the audit trail: the
	// act — somebody else reading this organisation's rows — is written there
	// by the consolidated run on both sides, and a second record of the same
	// fact would be a second thing to keep true. This app had the SQL for it
	// until 2026-08-23, over the platform's own audit_events and tenants.
	history, err := m.grants.History(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the access history")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"history": history})
}

// grantParty resolves the caller's tenant and the grant id together.
//
// The id is checked for shape here rather than in the domain: an unparseable
// identifier is a malformed request, in the same class as a body that is not
// JSON, and the answer is the same 400 either way.
func (m *Module) grantParty(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return "", "", false
	}
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid grant id")
		return "", "", false
	}
	return tenantID, id, true
}
