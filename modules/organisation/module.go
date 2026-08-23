/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package organisation is the organisation and the people in it.
 *
 * It was called `core` until the ecosystem split made the name a liability:
 * "core" is what the platform underneath every app is called, and an app named
 * after the floor it stands on cannot be told apart from it in a catalogue,
 * a permission code or an import path. What this module actually holds is the
 * organisation, its departments and its people — so that is what it is called.
 *
 * What is left here is the half that really is an app. The other half — the
 * tenant's legal profile and the signed-in person's own preferences — moved to
 * the platform (internal/platform/tenant_profile_handlers.go), because the
 * control plane, the XYP rail and an SSO consent screen all read the
 * organisation's registered name without caring which apps the tenant has, and
 * a screen those depend on cannot be one an administrator is able to remove.
 *
 * The shape follows Odoo's, because the distinctions it draws are real:
 *
 *	res.company   → tenants + tenant_profiles   what the organisation is  (platform)
 *	res.users     → users                       who the person is, anywhere (platform)
 *	hr.employee   → memberships                 who they are *here*         (here)
 *	hr.department → departments                 how it is arranged          (here)
 *
 * The middle line is the one worth keeping straight. A language preference
 * belongs to a person and follows them between organisations; a job title does
 * not. The same person can be a director in one tenant and a clerk in another,
 * so the title lives on the membership and the language lives on the user.
 *
 * Unlike Odoo's `base`, this one can be removed. Nothing imports it and no
 * other module's foreign keys point at a department, so a deployment that has
 * no use for an internal directory — a queue kiosk, a single-purpose portal —
 * should not be made to carry one. It is installed by default and uninstalling
 * it closes the gate without dropping a row.
 *
 * What this module does not do is authorisation. Roles and permissions already
 * exist and are already good (rbac + Settings → Access control); this names the
 * people those roles are handed to.
 */

package organisation

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"

	org "github.com/gerege-systems/client-gerege-nexus/domain/organisation"
	"github.com/gerege-systems/client-gerege-nexus/domain/organisation/postgres"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// ID is the catalogue identifier. It is referenced by the platform, which
// installs this app for every tenant and refuses to disable it.
const ID = "io.gerege.nexus.organisation"

// LegacyID is what this app was called before the rename. It is not a second
// identity: appcatalog resolves it to ID when a catalogue or an installation
// still carries it, so a deployment that has not yet run the migration and a
// registry that has not yet republished both keep working.
//
// DEPRECATED: remove in vNEXT.
const LegacyID = "io.gerege.nexus.core"

// Module is the platform's half of this app: the seven methods a catalogue
// entry has to answer, the routes, and one service that holds everything those
// routes are for.
//
// The database handle is not a field any more. It goes into the store at
// construction and nothing above that layer has one, which is what makes the
// grep in CI — no SQL under internal/apps/organisation — a property rather
// than a habit.
type Module struct {
	svc   *org.Service
	perms nexus.PermissionStore
}

// directoryOf is the platform's record of who belongs to an organisation.
//
// Asked for rather than queried. This module used to read users, memberships,
// roles, membership_roles and tenants with its own SQL — five of the platform's
// most careful tables, on a dependency no compiler could see. See
// pkg/nexus/directory.go and migration 00076.
func directoryOf() nexus.Directory {
	people, err := nexus.People()
	if err != nil {
		slog.Warn("organisation: this deployment provides no directory; the staff screens will refuse", "error", err)
	}
	return people
}

// The app's own schema: `departments` and `organisation_people`. Both were in
// the platform's db/migrations until this module left it — see the platform's
// 00077 and the header of 00001_organisation.sql.
//
//go:embed migrations/*.sql
var migrations embed.FS

// schema is the embedded directory, one level down. It panics on a broken
// embed for the reason the documents module gives: a module that meant to
// bring its tables and silently did not is worse than one that never did.
func schema() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("organisation: embedded migrations are unreadable: " + err.Error())
	}
	return sub
}

func New(p nexus.Platform) *Module {
	m := &Module{svc: org.NewService(repository{Store: postgres.New(p.DB()), people: directoryOf()}), perms: p.Permissions()}
	nexus.Register(m)
	nexus.Migrations(m.ID(), schema())
	registerReports()
	return m
}

func (m *Module) ID() string { return ID }

// Name is the organisation: what it is, how it is arranged, who is in it.
//
// It was "Organisation & People", then "Directory" for the few hours the
// contact register lived inside it. The register went to the commerce
// distribution, on the argument the merge had missed: departments and staff are
// something every organisation has, and customers are something a business has.
// One of those belongs in the platform and the other does not.
func (m *Module) Name() string { return "Organisation" }

// 3.0.0, not back to 1.0.0. Versions only move forwards: 2.0.0 was published,
// deployments installed it, and a catalogue that offered 1.0.0 to them would be
// offering a downgrade nothing knows how to apply.
func (m *Module) Version() string { return "3.0.0" }

func (m *Module) Dependencies() []nexus.Dependency { return nil }

// Permissions are deliberately two, not six.
//
// Reading who works here is something any member of the organisation may do —
// a directory nobody can open is not a directory. Changing the organisation's
// legal identity, its structure, or who belongs to it is administrative, and it
// is one decision rather than three: anybody who can add a person to a
// department can already put them anywhere in it.
func (m *Module) Permissions() []nexus.PermissionDefinition {
	return []nexus.PermissionDefinition{
		{Code: "organisation.read", Name: "Read Organisation", Description: "View the organisation profile, its departments and its people"},
		{Code: "organisation.manage", Name: "Manage Organisation", Description: "Edit the organisation profile, its departments and its people"},
	}
}

// Menus are the three screens this app is. No parent is named: the platform
// hangs an app's own menus under its Modules group, and a ParentID set here
// would be overwritten — which reads as a decision that was silently ignored.
func (m *Module) Menus() []nexus.MenuDefinition {
	return []nexus.MenuDefinition{
		{
			ID: "organisation_people", Label: "People",
			Path: "/organisation/people", Icon: "users", Order: 7,
			Labels: map[string]string{
				"mn": "Ажилтнууд", "ar": "الأشخاص", "zh": "人员",
				"fr": "Personnes", "ru": "Сотрудники", "es": "Personas",
			},
		},
		{
			ID: "organisation_departments", Label: "Departments",
			Path: "/organisation/departments", Icon: "network", Order: 6,
			Labels: map[string]string{
				"mn": "Хэлтэс, нэгж", "ar": "الأقسام", "zh": "部门",
				"fr": "Départements", "ru": "Подразделения", "es": "Departamentos",
			},
		},
	}
}

// RegisterRoutes mounts the app at its own name.
//
// The prefix is part of the rename: an app called `organisation` answering on
// /api/v1/core would keep the old name in the one place clients copy it from.
// The old prefix is still served for a release, but by the platform rather than
// from here — see tenantLegacyRoutes in internal/platform, which redirects it.
// It has to be the platform's, because half of what used to live under
// /api/v1/core is now a platform route that outlives this app being removed.
func (m *Module) RegisterRoutes(r chi.Router, tenantAuthMiddleware func(http.Handler) http.Handler) {
	r.Route("/api/v1/organisation", func(cr chi.Router) {
		cr.Use(tenantAuthMiddleware)
		read := nexus.RequirePermission(m.perms, "organisation.read")
		manage := nexus.RequirePermission(m.perms, "organisation.manage")

		// How it is arranged.
		cr.With(read).Get("/departments", m.handleListDepartments)
		cr.With(manage).Post("/departments", m.handleCreateDepartment)
		cr.With(manage).Put("/departments/{id}", m.handleUpdateDepartment)
		// Archiving and deleting are different acts and now say so. DELETE
		// used to archive, which left the screen with a Delete that did not
		// delete and no way to actually remove a unit created by mistake.
		cr.With(manage).Post("/departments/{id}/archive", m.handleArchiveDepartment)
		cr.With(manage).Post("/departments/{id}/restore", m.handleRestoreDepartment)
		cr.With(manage).Delete("/departments/{id}", m.handleDeleteDepartment)

		// Who is in it.
		cr.With(read).Get("/people", m.handleListPeople)
		cr.With(manage).Put("/people/{id}", m.handleUpdatePerson)
		cr.With(manage).Post("/people/{id}/deactivate", m.handleDeactivatePerson)
		cr.With(manage).Post("/people/{id}/reactivate", m.handleReactivatePerson)
	})
}
