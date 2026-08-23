/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package egov is this platform's front door to the Mongolian state's systems.
 *
 * The pieces existed already and were scattered: the ХУР registry lookups were
 * two handlers in the platform's own route table, sitting between the AI
 * endpoints and the integrations manager; whether eID, ДАН and ХУР were even
 * configured was knowable only from the deployment's environment; and what had
 * been looked up was in the audit log with nothing pointing at it. Between them
 * they are a product surface — "how this organisation is connected to the
 * state, and what it has asked the state" — and it had no name and no screen.
 *
 * What is here and what is deliberately not:
 *
 *	here     the registry lookups, the state of the three rails, and the
 *	         history of what this organisation looked up;
 *	platform the sign-in flows themselves (eID, ДАН, identity binding). They
 *	         run before anybody is signed in, so they cannot sit behind a gate
 *	         that asks which apps a tenant installed;
 *	platform a person's own list of linked identities and the button that
 *	         unlinks one. That is the account holder's, not their employer's,
 *	         and putting it behind an app an administrator can remove would
 *	         mean an administrator could take away somebody's ability to
 *	         detach their own national identity — see profile_handlers.go,
 *	         which has refused to be an app for the same reason since before
 *	         this module existed. This module links to it rather than owning
 *	         it;
 *	platform the low-level clients (platform/gerege, platform/eid,
 *	         platform/dan). They are infrastructure the platform itself signs
 *	         people in with; this module is their app-facing surface.
 */

package egov

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/egov"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// ID is the catalogue identifier.
const ID = "io.gerege.nexus.egov"

// Registry is what this module offers other modules in this binary.
//
// It is an interface and not the module type because the point of it is that
// nothing has to depend on this app being installed: a consumer holds a
// Registry or holds nil, and the nil case is a feature it does not offer rather
// than an error it reports. Contacts is the first caller-in-waiting — its
// address book fills itself in from the citizen registry — and it must keep
// working on a deployment that has no state integration at all.
//
// It is the domain's now, so that a consumer names this app's types rather than
// a platform client's to say what it got back.
type Registry = domain.Registry

// Rail and Rails moved to internal/platform/staterail: the platform builds the
// value and the platform is not allowed to import an app to name its type. See
// that package, and internal/apps/boundaries_test.go for the rule.

type Module struct {
	svc   *domain.Service
	perms nexus.PermissionStore
}

// New wires the two platform clients to the domain's ports.
//
// The clients stay the platform's — ХУР signs people in before any app is
// installed, and the rails are read from this process's configuration — so what
// crosses into the domain is not the client but the answer: a value with this
// app's own type on it, which is what lets the rules be run against a map.
func New(p nexus.Platform) *Module {
	// Asked of the platform rather than handed in. Both were parameters until
	// the contracts existed, and the ХУР one carried *gerege.GeregeService —
	// a type under internal/, which is what kept this module in this
	// repository. See pkg/nexus/stateregistry.go.
	state, err := nexus.StateRegistryOf()
	if err != nil {
		slog.Warn("egov: this deployment provides no state registry; lookups will refuse", "error", err)
	}
	rails, err := nexus.Capability[nexus.StateRails]()
	if err != nil {
		slog.Warn("egov: this deployment names no state rails", "error", err)
	}
	m := &Module{
		svc:   domain.NewService(registry{state: state}, asRails(rails), history{audit: auditReader()}),
		perms: p.Permissions(),
	}
	nexus.Register(m)
	return m
}

// auditReader is the trail this app reads its own history back from. A
// deployment that provides none leaves the history screen empty, which is what
// it should show when the trail cannot be read.
func auditReader() nexus.AuditReader {
	reader, err := nexus.AuditHistory()
	if err != nil {
		slog.Warn("egov: this deployment provides no audit reader; the lookup history will be empty", "error", err)
		return nil
	}
	return reader
}

// asRails is nexus.StateRails as domain/egov.Rails. The two structs are the same
// four fields with the same JSON, which is the point: neither side has to know
// the other, and the screen sees no difference.
func asRails(rails nexus.StateRails) domain.Rails {
	if rails == nil {
		return nil
	}
	return func() []domain.Rail {
		wired := rails()
		described := make([]domain.Rail, 0, len(wired))
		for _, rail := range wired {
			described = append(described, domain.Rail{
				ID: rail.ID, Name: rail.Name, Mode: rail.Mode, Endpoint: rail.Endpoint,
			})
		}
		return described
	}
}

// Compile-time proof that the module satisfies what it publishes.
var _ Registry = (*Module)(nil)

func (m *Module) Citizen(ctx context.Context, regNumber string) (domain.Citizen, error) {
	return m.svc.Citizen(ctx, regNumber)
}

func (m *Module) Company(ctx context.Context, companyReg string) (domain.Company, error) {
	return m.svc.Company(ctx, companyReg)
}

// registry is domain/egov.Registry over the platform's XYP client: a pointer
// and a nil check become a value and an error.
//
// The field copy is the translation, and it is written out rather than done by
// embedding so that a field added on the platform side does not silently become
// part of this app's published answer.
type registry struct{ state nexus.StateRegistry }

func (r registry) Citizen(ctx context.Context, regNumber string) (domain.Citizen, error) {
	if r.state == nil {
		return domain.Citizen{}, errors.New("this deployment is not connected to the state's registers")
	}
	info, err := r.state.Citizen(ctx, regNumber)
	if err != nil {
		return domain.Citizen{}, err
	}
	if info == nil {
		return domain.Citizen{}, errors.New("the registry answered with nothing")
	}
	return domain.Citizen{
		RegNumber: info.RegNumber, CivilID: info.CivilID,
		LastName: info.LastName, FirstName: info.FirstName,
		Gender: info.Gender, Address: info.Address,
		PassportStatus: info.PassportStatus, Verified: info.Verified,
	}, nil
}

func (r registry) Company(ctx context.Context, companyReg string) (domain.Company, error) {
	if r.state == nil {
		return domain.Company{}, errors.New("this deployment is not connected to the state's registers")
	}
	info, err := r.state.Company(ctx, companyReg)
	if err != nil {
		return domain.Company{}, err
	}
	if info == nil {
		return domain.Company{}, errors.New("the registry answered with nothing")
	}
	return domain.Company{
		CompanyReg: info.CompanyReg, Name: info.Name, Executive: info.Executive,
		Address: info.Address, VatPayer: info.VatPayer, Status: info.Status,
		FoundingDate: info.FoundingDate,
	}, nil
}

func (m *Module) ID() string { return ID }

// MenuPermission and RoutePermissionPrefix are this module's half of
// nexus.AccessPolicy — what the platform used to hold in a switch keyed by
// app ID, stated here so it survives the module moving to another repository.
// Route gating stays with the module: a citizen-registry lookup is a GET,
// and it must not be a `.read` that every member holds.
func (m *Module) MenuPermission() string        { return "egov.read" }
func (m *Module) RoutePermissionPrefix() string { return "" }
func (m *Module) Name() string                  { return "e-Government Link" }
func (m *Module) Version() string               { return "1.0.0" }

// Dependencies are none, in the direction that matters.
//
// Contacts reads the citizen registry through this module and does not depend
// on it: an address book that refuses to open because the state integration is
// missing would be a worse product than one that cannot pre-fill a form.
func (m *Module) Dependencies() []nexus.Dependency { return nil }

// Permissions: one to see the screens, two to ask the state a question.
//
// The two lookups are AdminOnly, which is the whole reason that field exists.
// They read another human being's record out of the national registry from
// nothing but their registration number, and the installer's default rule —
// anything ending `.read` goes to every member of the organisation — would
// have handed that to the whole staff on install. Before this module they were
// `xyp.citizen.read` and `xyp.company.read`, granted to the administrator role
// alone by migration 00024; this keeps that and says why in code rather than
// in a migration nobody re-reads.
func (m *Module) Permissions() []nexus.PermissionDefinition {
	return []nexus.PermissionDefinition{
		{Code: "egov.read", Name: "Read e-Government Link",
			Description: "See how this organisation is connected to the state's systems, and what it has looked up"},
		{Code: "egov.citizen.read", Name: "Query the citizen registry", AdminOnly: true,
			Description: "Look up authoritative citizen data through XYP"},
		{Code: "egov.company.read", Name: "Query the company registry", AdminOnly: true,
			Description: "Look up authoritative legal-entity data through XYP"},
	}
}

func (m *Module) Menus() []nexus.MenuDefinition {
	return []nexus.MenuDefinition{
		{ID: "egov_lookups", Label: "Registry lookups", Path: "/egov", Icon: "landmark", Order: 10,
			Labels: map[string]string{"mn": "Лавлагаа", "ar": "استعلامات السجل", "zh": "登记查询",
				"fr": "Consultations du registre", "ru": "Справки из реестра", "es": "Consultas al registro"}},
		{ID: "egov_connections", Label: "Connections", Path: "/egov/connections", Icon: "share-2", Order: 20,
			Labels: map[string]string{"mn": "Холболтууд", "ar": "الاتصالات", "zh": "连接",
				"fr": "Connexions", "ru": "Подключения", "es": "Conexiones"}},
		{ID: "egov_history", Label: "Lookup history", Path: "/egov/history", Icon: "scroll-text", Order: 30,
			Labels: map[string]string{"mn": "Түүх", "ar": "سجل الاستعلامات", "zh": "查询历史",
				"fr": "Historique", "ru": "История запросов", "es": "Historial"}},
	}
}

// RegisterRoutes mounts the app, and keeps the two pre-move URLs answering.
//
// The lookups were platform routes — /api/v1/xyp/* in the authenticated group —
// so the aliases move them behind this app's gate as well as renaming them. A
// tenant that removes this app loses the old URL too, which is the point:
// "uninstalled" has to mean the same thing at both addresses.
func (m *Module) RegisterRoutes(r chi.Router, tenantAuthMiddleware func(http.Handler) http.Handler) {
	r.Route("/api/v1/egov", func(er chi.Router) {
		er.Use(tenantAuthMiddleware)

		read := nexus.RequirePermission(m.perms, "egov.read")
		er.With(read).Get("/connections", m.handleConnections)
		er.With(read).Get("/history", m.handleHistory)

		er.With(nexus.RequirePermission(m.perms, "egov.citizen.read")).Post("/citizen", m.handleCitizen)
		er.With(nexus.RequirePermission(m.perms, "egov.company.read")).Post("/company", m.handleCompany)
	})

	// DEPRECATED: remove in vNEXT — the addresses these two lookups had while
	// they were platform routes.
	r.Route("/api/v1/xyp", func(xr chi.Router) {
		xr.Use(tenantAuthMiddleware)
		xr.With(nexus.RequirePermission(m.perms, "egov.citizen.read")).Post("/citizen", m.handleCitizen)
		xr.With(nexus.RequirePermission(m.perms, "egov.company.read")).Post("/company", m.handleCompany)
	})
}

func (m *Module) handleCitizen(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	var req struct {
		RegNumber string `json:"reg_number"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<13)).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid registration number")
		return
	}

	info, err := m.svc.Citizen(r.Context(), req.RegNumber)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, tenantID, domain.ActionCitizenQueried, map[string]any{"reg_number": req.RegNumber})
	nexus.JSON(w, http.StatusOK, info)
}

func (m *Module) handleCompany(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	var req struct {
		CompanyReg string `json:"company_reg"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<13)).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid company registration number")
		return
	}

	info, err := m.svc.Company(r.Context(), req.CompanyReg)
	if err != nil {
		fail(w, r, err)
		return
	}

	m.record(r, tenantID, domain.ActionCompanyQueried, map[string]any{"company_reg": req.CompanyReg})
	nexus.JSON(w, http.StatusOK, info)
}

// record keeps what was asked. The lookups are the whole reason the history
// screen has anything to read.
func (m *Module) record(r *http.Request, tenantID, action string, details map[string]any) {
	claims, _ := nexus.UserFromContext(r.Context())
	nexus.Audit(r.Context(), tenantID, claims.UserID, action, "egov", details)
}

// fail answers with the domain's own words. Everything this app refuses is the
// caller's request; everything else is a platform that could not answer.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	if domain.IsRefusal(err) {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Error("egov: "+err.Error(), "error", errors.Unwrap(err), "path", r.URL.Path)
	nexus.Error(w, http.StatusInternalServerError, err.Error())
}

// handleConnections answers with the three rails and where a person manages
// their own identity.
//
// The link out is part of the answer rather than something the client knows:
// this screen is the obvious place to look for "my eID", the control for it is
// deliberately not here, and a screen that mentions the thing without saying
// where it is would only send people looking through Settings.
func (m *Module) handleConnections(w http.ResponseWriter, r *http.Request) {
	if _, ok := nexus.RequireTenant(w, r); !ok {
		return
	}
	nexus.JSON(w, http.StatusOK, m.svc.Connections())
}

// handleHistory reads the audit trail rather than a table of its own.
//
// The lookups already write an audit event, and a second record of the same act
// would be a second thing to keep in step with the first. The audit table is
// also the one place a deletion is not expected: an organisation should not be
// able to tidy away the record of whose registry data it read.
func (m *Module) handleHistory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	lookups, err := m.svc.History(r.Context(), tenantID)
	if err != nil {
		fail(w, r, err)
		return
	}
	nexus.JSON(w, http.StatusOK, lookups)
}
