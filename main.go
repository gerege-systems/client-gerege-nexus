/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// Command clientnexus runs the Gerege Nexus platform as the Gerege Client
// distribution: the instance that does not identify anybody itself but signs
// people in at nexus.gerege.mn.
//
// The federation is configuration, not code — SSO_CLIENT_ISSUER and the four
// variables beside it in deploy/docker-compose.yml. Nothing in this file knows
// about it, which is the point: being a relying party is a deployment
// decision, and a distribution that compiled it in could not be pointed at a
// second provider.
//
// It carries six apps, and every one of them moved here from the platform: the
// e-Government link, the organisation and its people, documents with the
// signature ceremonies, the reports, Өртөө — the task board and, since
// 2026-08-27, the channel under it — and the connectors to systems outside the
// platform. The repository was Level 2 from the
// first commit precisely so that this would be a change to one file rather
// than a migration of the deployment, and it was.
//
// What is left in the platform after them is the platform: sign-in, tenants,
// the store, the rails. An app that ships with the core is an app every
// deployment carries whether it wants it or not, and the whole point of the
// boundary work is that nobody has to.
//
// Modules go in the Options.Modules callback below and nowhere else. Logic
// written in this file instead of in a module is logic no other deployment can
// have and no test can reach — the ecosystem strategy, §5 rule 3.
package main

import (
	"log/slog"
	"os"

	"github.com/gerege-systems/client-gerege-nexus/modules/documents"
	"github.com/gerege-systems/client-gerege-nexus/modules/egov"
	"github.com/gerege-systems/client-gerege-nexus/modules/integrations"
	"github.com/gerege-systems/client-gerege-nexus/modules/organisation"
	"github.com/gerege-systems/client-gerege-nexus/modules/reports"
	"github.com/gerege-systems/client-gerege-nexus/modules/urtuu"
	"github.com/gerege-systems/client-gerege-nexus/modules/urtuu/channel"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/host"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// signingRail is the platform's PDF signing surface, or the zero value.
//
// Asked for rather than built: nexus.Signer is a contract the platform
// publishes and this distribution has no way to implement — it needs the
// deployment's eID registration. A deployment without one gets a rail that
// answers Enabled() false, which is a state the documents app already handles:
// a document with no file is approved on the sign-in rail instead.
func signingRail() nexus.Signer {
	signer, err := nexus.Capability[nexus.Signer]()
	if err != nil {
		slog.Warn("this deployment publishes no signing rail; documents will be approved rather than signed", "error", err)
	}
	return signer
}

// The four platform surfaces the reports app is built on. Each is asked for
// rather than built: an engine of its own would be a second reader of the same
// rows, and the records are the platform's because the platform acts on them.
func reportEngine() nexus.ReportEngine {
	engine, err := nexus.Capability[nexus.ReportEngine]()
	if err != nil {
		slog.Error("this deployment provides no report engine; reports will not run", "error", err)
	}
	return engine
}

func reportSchedules() nexus.ReportSchedules {
	schedules, err := nexus.Capability[nexus.ReportSchedules]()
	if err != nil {
		slog.Error("this deployment keeps no report schedules", "error", err)
	}
	return schedules
}

func reportGrants() nexus.ReportGrants {
	grants, err := nexus.Capability[nexus.ReportGrants]()
	if err != nil {
		slog.Error("this deployment keeps no report sharing agreements", "error", err)
	}
	return grants
}

// installedApps is the platform's own per-tenant gate: a tenant sees the
// reports of the apps it has installed and no others, and "which apps" has
// exactly one answer on this deployment.
func installedApps() nexus.InstalledApps {
	installed, err := nexus.Capability[nexus.InstalledApps]()
	if err != nil {
		slog.Error("this deployment cannot say which apps an organisation has", "error", err)
	}
	return installed
}

func main() {
	// The error is checked and the exit code is the point: a distribution that
	// cannot start — a catalogue that disagrees with its modules, a database it
	// cannot reach — must not exit 0 and read as a clean shutdown to whatever
	// is supervising it.
	err := host.Run(host.Options{
		// Every module this distribution carries, constructed here and nowhere
		// else. A module registers itself; what this callback decides is which
		// ones exist in this binary at all.
		Modules: func(p nexus.Platform) {
			// organisation first, the way the platform built it: it is the
			// organisation, its departments and its people — the module Odoo
			// calls base — and the others read a directory it presents.
			organisation.New(p)
			// documents is handed the signing rail rather than asking for it.
			// The rail is the platform's — ADR 0002 is about why there is
			// exactly one — and this is the line that says which app mounts
			// the routes for it.
			documents.New(p, signingRail())
			egov.New(p)
			// The connectors: webhook subscribers, and the Drive, Dropbox and
			// Meet accounts an organisation links. They were nine routes in the
			// platform's own server until 2026-08-27 — see the package comment
			// for the two arguments that kept them there and why neither held.
			//
			// Nothing is handed in. The one thing this app cannot own is the
			// cipher that seals a stored credential, and it asks the platform
			// for that per call (nexus.SecretSealer) rather than holding it:
			// modules are built before main() has finished publishing.
			integrations.New(p)
			// Өртөө: the task board and, since 2026-08-27, the channel
			// underneath it. Both halves are this product's now — the platform
			// published nexus.Link and nexus.PeerDirectory for three months and
			// this app was the only caller either ever had, which is what made
			// them one app's transport rather than a platform rail.
			//
			// Two values rather than one, and they are still different halves:
			// the channel is how work is sent, the directory is how the other
			// end is read. The interfaces stayed (domain/urtuu/wire) because
			// the board's tests replace both with two structs in one process.
			//
			// Built whether or not this deployment has a signing key. Without
			// one the channel answers Enabled() false and carries nothing; the
			// module still registers its readers, so an installation given a
			// key later does not need a second restart before its backlog is
			// read.
			ring := channel.New(p.DB(), p.Permissions())
			urtuu.New(p, ring, channel.AsPeerDirectory(ring))
			// Reports last, and after every module that registers one: the app
			// serves the registry, and a module constructed after it would have
			// its reports missing from the first listing.
			//
			// Four things asked of the platform rather than built: the engine
			// runs the SQL, and the schedules and agreements are rows the
			// platform acts on — the sweep mails one at three in the morning,
			// the consolidated run checks the other before reading another
			// organisation's data.
			reports.New(p, installedApps(), reportEngine(), reportSchedules(), reportGrants())
		},

		// Every organisation gets the staff directory without asking. It was
		// the platform's only default app until the module moved here; the
		// list moved with it, which is the whole reason Options.DefaultApps
		// exists.
		//
		// documents and egov are not on it. A document register and a link to
		// the state's registers are things an organisation chooses, and the
		// store is where it chooses them.
		DefaultApps: []string{"io.gerege.nexus.organisation"},
	})
	if err != nil {
		slog.Error("gerege client stopped", "error", err)
		os.Exit(1)
	}
}
