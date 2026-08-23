/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package reports is the app that serves every other module's reports.
 */

// The reports module knows nothing about billing, inventory or e-signatures.
//
// It serves whatever is in the reporting registry, gated by which apps the
// caller's tenant has installed. That is the Odoo shape: a report ships with
// the module that understands the data, and the reporting layer is generic.
// The alternative — this module importing every other one to know their
// reports — is exactly the coupling the compile-time module registry was built
// to avoid.
package reports

import (
	"net/http"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
)

// ID is the app id, in the catalogue and in every installation row.
const ID = "io.gerege.nexus.reports"

// Permissions. Two, and the split matters: viewing a report is an ordinary act
// for anybody who works with the data, while a schedule sends this
// organisation's numbers to an address list on a timer, which is an
// administrative decision.
const (
	PermissionView     = "reports.view"
	PermissionSchedule = "reports.schedule"
	// PermissionShare covers both directions of a sharing agreement: asking
	// another organisation to show you a report, and agreeing to show them
	// yours. Separate from the other two because it is the only one that
	// crosses the tenant boundary at all.
	PermissionShare = "reports.share"
)

// InstalledApps answers which apps a tenant has, which is how the module
// decides which reports exist for them. Supplied by the platform rather than
// queried here, because the platform already caches that answer and this module
// must not become a second, differently-stale source of it.
//
// An alias for the SDK's: the capability is keyed on the type, so a named type
// of this module's own would be a key the platform never provides. The core
// learned that the same way, with the PDF signing rails.
type InstalledApps = nexus.InstalledApps

// Module is the reports app: the catalogue entry, the routes, the engine it
// serves and the rules it applies before it does.
//
// Four contracts and no database handle. The rows this app shows are the
// platform's — a schedule is mailed by the platform's sweep, a grant is checked
// by the platform's consolidated run, and the audit trail behind the access
// history is the platform's too — so all three are read through contracts
// rather than with SQL of this app's own. That is what lets these screens run
// anywhere the platform does.
type Module struct {
	engine    nexus.ReportEngine
	schedules nexus.ReportSchedules
	grants    nexus.ReportGrants
	perms     nexus.PermissionStore
	svc       *domain.Service
}

// New builds the module and registers it.
//
// installedApps is the platform's own gate, handed in: a tenant sees the
// reports of the apps it has installed and no others, and "which apps" has
// exactly one answer on this deployment.
func New(p nexus.Platform, installedApps InstalledApps,
	engine nexus.ReportEngine, schedules nexus.ReportSchedules, grants nexus.ReportGrants) *Module {

	m := &Module{
		engine:    engine,
		schedules: schedules,
		grants:    grants,
		perms:     p.Permissions(),
		svc: domain.NewService(
			catalogue{engine},
			// The store is the two record contracts, translated: the domain
			// says what a schedule and an agreement have to satisfy, and the
			// platform keeps them.
			records{schedules: schedules, grants: grants},
			domain.Installations(installedApps),
		),
	}
	nexus.Register(m)
	return m
}

func (m *Module) ID() string      { return ID }
func (m *Module) Name() string    { return "Reports" }
func (m *Module) Version() string { return "1.0.0" }

// Dependencies is empty on purpose. Reports is useful with any combination of
// the other apps and useless with none of them, and declaring a dependency on
// billing would mean a tenant that only runs inventory could not install it.
func (m *Module) Dependencies() []nexus.Dependency { return nil }

func (m *Module) Permissions() []nexus.PermissionDefinition {
	return []nexus.PermissionDefinition{
		{Code: PermissionView, Name: "View Reports", Description: "Run and export the reports of the apps this organisation has installed"},
		{Code: PermissionSchedule, Name: "Schedule Reports", Description: "Create and remove scheduled reports that are mailed out automatically"},
		{Code: PermissionShare, Name: "Share Reports Across Organisations", Description: "Ask another organisation to share a report, and agree to share this organisation's own"},
	}
}

func (m *Module) Menus() []nexus.MenuDefinition {
	return []nexus.MenuDefinition{
		{
			ID: "reports", ParentID: "operations", Label: "Reports", Path: "/reports",
			Icon: "bar-chart-3", Order: 90,
			Labels: map[string]string{
				"mn": "Тайлан", "ar": "التقارير", "zh": "报表",
				"fr": "Rapports", "ru": "Отчёты", "es": "Informes",
			},
		},
	}
}

// The schedule sweep was started here until 2026-08-23 and is the platform's
// now. A screen was responsible for a deployment's housekeeping, which meant an
// organisation that removed this app quietly stopped receiving the schedules it
// still had.

// RegisterRoutes mounts the API behind the app gate.
//
// Every route is inside gateMiddleware: there is no public reporting endpoint
// and there must never be one — a report is an aggregate of a tenant's data,
// which is the thing this platform is most careful about.
func (m *Module) RegisterRoutes(r chi.Router, gateMiddleware func(http.Handler) http.Handler) {
	r.Route("/api/v1/reports", func(rr chi.Router) {
		rr.Use(gateMiddleware)

		// Reading. `reports.view` plus, per report, the app it belongs to —
		// checked in the handler rather than here, because which app depends
		// on which report was asked for.
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Get("/", m.handleList)
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Get("/schedules", m.handleListSchedules)
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Get("/{key}", m.handleMetadata)
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Post("/{key}/run", m.handleRun)
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Post("/{key}/export", m.handleExport)

		// The consolidated view. Reading, from this organisation's side: the
		// permission that matters on the other side is the grant, and it was
		// given by the organisation that owns the data rather than by anything
		// here.
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Post("/{key}/run-consolidated", m.handleRunConsolidated)
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Get("/grants", m.handleListGrants)
		// Who has read our data. A read, and one this organisation is entitled
		// to — see handleAccessHistory.
		rr.With(nexus.RequirePermission(m.perms, PermissionView)).Get("/grants/history", m.handleAccessHistory)

		// Scheduling. Administrative: it sends this organisation's numbers to
		// an address list, on a timer, without anybody present.
		rr.With(nexus.RequirePermission(m.perms, PermissionSchedule)).Post("/schedules", m.handleCreateSchedule)
		rr.With(nexus.RequirePermission(m.perms, PermissionSchedule)).Put("/schedules/{id}", m.handleUpdateSchedule)
		rr.With(nexus.RequirePermission(m.perms, PermissionSchedule)).Delete("/schedules/{id}", m.handleDeleteSchedule)

		// Sharing. Under reports.share, not reports.schedule: asking another
		// organisation for their numbers, and agreeing to hand over your own,
		// is a different decision from mailing your own out — and the second
		// one is the one this platform is most careful about.
		rr.With(nexus.RequirePermission(m.perms, PermissionShare)).Post("/grants", m.handleRequestGrant)
		rr.With(nexus.RequirePermission(m.perms, PermissionShare)).Post("/grants/{id}/accept", m.handleAcceptGrant)
		rr.With(nexus.RequirePermission(m.perms, PermissionShare)).Post("/grants/{id}/revoke", m.handleRevokeGrant)
	})
}
