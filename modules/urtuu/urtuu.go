/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package urtuu is the Өртөө app: the task board, and the channel under it.
 *
 * The split the proposal drew (§3) put the channel in the platform —
 * infrastructure, on the argument that any module may one day need to hand work
 * to another installation, and that a link an administrator established must
 * keep carrying what is in flight over it whatever apps come and go. Three
 * months later the ring still had one caller, this one, so what the platform
 * was carrying for everybody was one app's transport. The channel came here on
 * 2026-08-27 (modules/urtuu/channel) and the core kept nothing of Өртөө: no
 * tables, no routes, no contract, no environment variables.
 *
 * The split itself survives the move, one directory down: this package holds
 * the product — a task, its life, its tree, the screens — and reaches the
 * channel only through the two interfaces in domain/urtuu/wire. That is what
 * lets the lifecycle tests run two installations as two structs in one
 * process, and it is what keeps a board query from growing a join into the
 * outbox.
 *
 * A tenant installs this the way it installs anything else. What is different
 * from every other app is that uninstalling it now stops the exchange as well
 * as hiding the board: the envelopes in flight stay in the queue, and nothing
 * carries them until it is installed again.
 */

package urtuu

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
)

// The app's own schema: the task, its events and the year's numbering. All
// three were in the platform's db/migrations until this module left it — see
// the header of 00001_urtuu.sql. What is not here is the channel's own five
// tables, which stayed with the channel.
//
//go:embed migrations/*.sql
var migrations embed.FS

// schema is the embedded directory, one level down. It panics on a broken
// embed: a module that meant to bring its tables and silently did not is worse
// than one that never did.
func schema() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("urtuu: embedded migrations are unreadable: " + err.Error())
	}
	return sub
}

// ID is the catalogue identifier.
const ID = "io.gerege.nexus.urtuu"

// Module is the app.
//
// It holds the transport rather than talking HTTP itself: everything that
// crosses an installation boundary goes through Enqueue and comes back through
// a registered reader, so this package contains no network code at all and
// cannot accidentally grow a second way to reach a peer.
type Module struct {
	db    nexus.DB
	perms nexus.PermissionStore
	link  contract.Link
	// peers is the reading half of the same channel: who is on the other end of
	// a link, what a code means, what went over it. Handed in rather than
	// fetched from the registry for the reason link is — a dependency a
	// constructor names is one the compiler checks, and one fetched inside a
	// method is a nil five minutes after a clean boot.
	peers contract.PeerDirectory
}

// New builds the module and registers what it reads.
//
// The readers are registered even on a deployment with no signing key. Nothing
// will arrive, and that is the point: whether this module is wired up must not
// depend on configuration, or an installation that is given a key later would
// need a restart before its readers existed — which is exactly the sort of
// thing that is discovered by a backlog nobody processed.
// link is the installation ring, as pkg/nexus publishes it.
//
// The interface rather than the platform's own *transport.Service: this app is
// meant to be able to leave for a distribution of its own, and a distribution
// cannot import internal/. Five methods is all it ever used, which is what made
// the capability worth publishing — see contract.Link.
func New(p nexus.Platform, link contract.Link, peers contract.PeerDirectory) *Module {
	m := &Module{db: p.DB(), perms: p.Permissions(), link: link, peers: peers}
	nexus.Register(m)
	nexus.Migrations(m.ID(), schema())
	link.Deliver(contract.KindTaskAssigned, m.receiveAssignment)
	link.Deliver(contract.KindTaskUpdate, m.receiveUpdate)
	registerReports(peers)
	return m
}

func (m *Module) ID() string   { return ID }
func (m *Module) Name() string { return "Urtuu Relay" }

func (m *Module) Version() string { return "1.1.0" }

// Dependencies are none. A task board needs the channel, and the channel is
// inside this app — so there is nothing here that could be uninstalled out from
// under it.
func (m *Module) Dependencies() []nexus.Dependency { return nil }

// Permissions are three, and the third is the interesting one.
//
// Reading the queues is something any member of the organisation may do: a
// board nobody can see is not a board. Managing — establishing links, opening
// codes, raising work and sending it — is administrative in the ordinary sense
// and follows the platform's default rule to the manager role.
//
// Processing is deliberately neither. Accepting a task is this organisation
// committing to another organisation that the work will be done, and returning
// one is refusing in its name; both are answered for outside these walls. The
// installer's default rule grants `.read` to every member and `.manage` to
// managers, and `urtuu.process` matches neither pattern — so it reaches nobody
// by default and an administrator hands it to the people who actually answer
// for the work. That is the intended outcome, not an oversight.
func (m *Module) Permissions() []nexus.PermissionDefinition {
	return []nexus.PermissionDefinition{
		{Code: "urtuu.read", Name: "Read Urtuu",
			Description: "See the incoming and outgoing task queues, the links and the dashboard"},
		{Code: "urtuu.manage", Name: "Manage Urtuu",
			Description: "Establish links to other installations, open request codes, and raise and send tasks"},
		{Code: "urtuu.process", Name: "Process Urtuu Tasks",
			Description: "Accept, return, delegate and complete tasks this organisation has been given"},
	}
}

// Menus are the four screens. The dashboard is last in the list and first in
// order: it is the one somebody opens in the morning.
func (m *Module) Menus() []nexus.MenuDefinition {
	return []nexus.MenuDefinition{
		{
			ID: "urtuu_board", Label: "Urtuu board",
			Path: "/module/urtuu", Icon: "route", Order: 1,
			Labels: map[string]string{
				"mn": "Самбар", "ar": "اللوحة", "zh": "总览",
				"fr": "Tableau de bord", "ru": "Сводка", "es": "Panel",
			},
		},
		{
			ID: "urtuu_incoming", Label: "Incoming tasks",
			Path: "/module/urtuu/incoming", Icon: "inbox", Order: 2,
			Labels: map[string]string{
				"mn": "Ирсэн даалгавар", "ar": "المهام الواردة", "zh": "收到的任务",
				"fr": "Tâches reçues", "ru": "Входящие задания", "es": "Tareas recibidas",
			},
		},
		{
			ID: "urtuu_outgoing", Label: "Sent tasks",
			Path: "/module/urtuu/outgoing", Icon: "send", Order: 3,
			Labels: map[string]string{
				"mn": "Илгээсэн даалгавар", "ar": "المهام المرسلة", "zh": "发出的任务",
				"fr": "Tâches envoyées", "ru": "Отправленные задания", "es": "Tareas enviadas",
			},
		},
		{
			ID: "urtuu_links", Label: "Links",
			Path: "/module/urtuu/links", Icon: "link-2", Order: 4,
			Labels: map[string]string{
				"mn": "Холбоосууд", "ar": "الروابط", "zh": "连接",
				"fr": "Liens", "ru": "Связи", "es": "Enlaces",
			},
		},
	}
}

// MenuPermission hides the app's entries from members who cannot read it.
func (m *Module) MenuPermission() string { return "urtuu.read" }

// RoutePermissionPrefix is deliberately empty. The verb does not decide the
// rule here: accepting a task is a POST and so is sending one, and they are
// different authorities held by different people. Each route names its own —
// see RegisterRoutes.
func (m *Module) RoutePermissionPrefix() string { return "" }

func (m *Module) RegisterRoutes(r chi.Router, tenantAuthMiddleware func(http.Handler) http.Handler) {
	// The channel's own routes, when what we were handed is a channel: the
	// four another installation reaches and the two an administrator here
	// does. An optional interface because the board's tests hand over a fake
	// that has no sockets and no screens, and there is nothing for it to mount.
	if mounts, ok := m.link.(interface {
		Routes(chi.Router, func(http.Handler) http.Handler)
	}); ok {
		mounts.Routes(r, tenantAuthMiddleware)
	}

	r.Route("/api/v1/urtuu/tasks", func(tr chi.Router) {
		tr.Use(tenantAuthMiddleware)
		read := nexus.RequirePermission(m.perms, "urtuu.read")
		manage := nexus.RequirePermission(m.perms, "urtuu.manage")
		process := nexus.RequirePermission(m.perms, "urtuu.process")

		tr.With(read).Get("/", m.handleListTasks)
		tr.With(read).Get("/board", m.handleBoard)
		tr.With(read).Get("/{id}", m.handleGetTask)

		// Raising work and sending it downward is a manage act: it commits
		// another organisation's time.
		tr.With(manage).Post("/", m.handleCreateTask)
		tr.With(manage).Post("/{id}/delegate", m.handleDelegate)
		// Closing is the originator accepting the outcome, which is the same
		// authority that raised it.
		tr.With(manage).Post("/{id}/close", m.handleClose)

		// Answering for work this organisation has been given.
		tr.With(process).Post("/{id}/accept", m.handleAccept)
		tr.With(process).Post("/{id}/return", m.handleReturn)
		tr.With(process).Post("/{id}/assign", m.handleAssign)
		tr.With(process).Post("/{id}/complete", m.handleComplete)
	})
}

// StartHousekeeping runs the channel's exchange loop and its retention sweep.
//
// The platform calls this on every registered module that has it — see the
// optional interface in the workspace service's StartBackgroundJobs. It is the
// whole reason that hook exists: without the loop a child installation never
// asks its parent for anything, and what breaks is a queue that quietly stays
// full rather than a route that answers 500.
//
// Nothing starts where the channel is switched off, and nothing at all starts
// under a fake: both are the same optional interface as the routes above.
func (m *Module) StartHousekeeping(ctx context.Context) {
	if background, ok := m.link.(interface{ StartHousekeeping(context.Context) }); ok {
		background.StartHousekeeping(ctx)
	}
}
