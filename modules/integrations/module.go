/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package integrations connects an organisation to systems outside the
 * platform: webhook subscribers, and the SaaS accounts a file or an
 * appointment has to reach — Google Drive, Dropbox and Google Meet.
 *
 * It was nine routes in the platform's server.go and a manager built before
 * every module, on the argument that the PDF signing rail filed documents
 * through it and nexus.MeetingBooker was its adapter. Neither survived contact
 * with the question a rail has to answer, which is whether more than one thing
 * needs it: MeetingBooker was never called by anything, and the export had one
 * caller — the rail itself — which would have had to ask a capability registry
 * whether somebody had installed an app. So the connectors became an app here
 * on 2026-08-27, and the core kept only the cipher, because a deployment must
 * have exactly one of those (nexus.SecretSealer).
 *
 * What that costs, said plainly: a signed PDF is no longer filed automatically
 * to a connected Drive, and POST /esign/documents/{id}/export is gone. The
 * export code is still here and still works — ExportFile and ExportFileToAll —
 * and the day something in this repository wants to file a document, it is one
 * call away rather than a contract away.
 */

package integrations

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
)

// The app's own schema: the connectors, the OAuth states and the delivery log.
// All three were in the platform's db/migrations until this module left it —
// see the header of 00001_connectors.sql.
//
//go:embed migrations/*.sql
var migrations embed.FS

func schema() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("integrations: embedded migrations are unreadable: " + err.Error())
	}
	return sub
}

// ID is the catalogue identifier. Unchanged from when the app card was created
// on 2026-08-23: the same product, one repository further out.
const ID = "io.gerege.nexus.integrations"

// Module is the app.
type Module struct {
	mgr      *Manager
	handler  *Handler
	perms    nexus.PermissionStore
	platform nexus.Platform
}

// New builds the connectors app.
func New(p nexus.Platform) *Module {
	mgr := NewManager(p.DB())
	m := &Module{mgr: mgr, handler: NewHandler(mgr), perms: p.Permissions(), platform: p}
	nexus.Register(m)
	nexus.Migrations(m.ID(), schema())
	return m
}

// Connectors is the manager, for a sibling module in this repository that has
// something to file or a meeting to book.
//
// An accessor rather than a published capability: a capability with no consumer
// is what this app's own MeetingBooker was for a year, and the lesson written
// into pkg/nexus/capability.go is that half a contract is none. When something
// here calls this, it will be a line in main.go and a field on that module.
func (m *Module) Connectors() *Manager { return m.mgr }

func (m *Module) ID() string      { return ID }
func (m *Module) Name() string    { return "Integrations" }
func (m *Module) Version() string { return "1.1.0" }

// Dependencies are none. A connector is a URL or an account and knows nothing
// about the rest of this deployment.
func (m *Module) Dependencies() []nexus.Dependency { return nil }

// Permissions are one, and it is administrative.
//
// Registering a connector means naming an address this server will connect to
// from inside the network, and holding a credential for somebody's Drive. That
// is not a thing a member of an organisation does; it is a thing whoever runs
// the organisation's systems does. AdminOnly keeps it off the manager and user
// roles at install time — the installer would otherwise read the `.manage`
// suffix and hand it to every manager.
func (m *Module) Permissions() []nexus.PermissionDefinition {
	return []nexus.PermissionDefinition{
		{Code: "integrations.manage", Name: "Manage connectors", AdminOnly: true,
			Description: "Register, connect and remove links to systems outside this platform"},
	}
}

// Menus is the one screen, under the app's settings header rather than beside
// the work screens: nobody opens the connectors list in the morning.
func (m *Module) Menus() []nexus.MenuDefinition {
	return []nexus.MenuDefinition{
		{
			ID: "integrations_connectors", Label: "Connectors",
			Path: "/module/integrations/connectors", Icon: "link-2", Order: 1,
			Group: nexus.MenuGroupSettings,
			Labels: map[string]string{
				"mn": "Холбогчид", "ar": "الموصلات", "zh": "连接器",
				"fr": "Connecteurs", "ru": "Коннекторы", "es": "Conectores",
			},
		},
	}
}

// MenuPermission hides the entry from anybody who cannot use it.
func (m *Module) MenuPermission() string { return "integrations.manage" }

// RoutePermissionPrefix is empty: every route below names its own, and one of
// them deliberately has none.
func (m *Module) RoutePermissionPrefix() string { return "" }

func (m *Module) RegisterRoutes(r chi.Router, workspaceAuth func(http.Handler) http.Handler) {
	// The provider's redirect. It arrives on somebody's browser with none of
	// our session on it — the provider sent them, not us — so it sits outside
	// the authentication middleware, and the single-use `state` row is the
	// whole of the authority. It was on the platform's public-route allowlist
	// for the same reason; that list is in the core and no longer names this,
	// so the reason is written here instead.
	r.Get("/api/v1/integrations/oauth/callback", m.handler.HandleOAuthCallback)

	r.Route("/api/v1/integrations", func(ir chi.Router) {
		ir.Use(workspaceAuth)
		manage := nexus.RequirePermission(m.perms, "integrations.manage")

		ir.With(manage).Get("/", m.handler.HandleList)
		ir.With(manage).Post("/", m.handler.HandleRegister)
		ir.With(manage).Get("/providers", m.handler.HandleProviders)
		ir.With(manage).Get("/deliveries", m.handler.HandleDeliveries)
		ir.With(manage).Put("/{id}", m.handler.HandleUpdate)
		ir.With(manage).Delete("/{id}", m.handler.HandleDelete)
		ir.With(manage).Post("/{id}/connect", m.handler.HandleConnect)
		ir.With(manage).Post("/{id}/disconnect", m.handler.HandleDisconnect)
	})
}

// StartHousekeeping sweeps the two tables that only ever grow: abandoned
// connect attempts, and the delivery log past its retention.
//
// The platform calls this on every registered module that has it. Before that
// hook existed the platform called it directly, which is what a module in
// another repository could not arrange.
func (m *Module) StartHousekeeping(ctx context.Context) { m.mgr.StartHousekeeping(ctx) }
