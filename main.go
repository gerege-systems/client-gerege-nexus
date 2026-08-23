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
// It carries three apps, all of which moved here from the platform on
// 2026-08-23: the e-Government link, the organisation and its people, and
// documents with the signature ceremonies. The repository was Level 2 from the
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
	"github.com/gerege-systems/client-gerege-nexus/modules/organisation"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/platform"
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

func main() {
	// The error is checked and the exit code is the point: a distribution that
	// cannot start — a catalogue that disagrees with its modules, a database it
	// cannot reach — must not exit 0 and read as a clean shutdown to whatever
	// is supervising it.
	err := platform.Run(platform.Options{
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
