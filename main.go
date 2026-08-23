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
// It carries the e-Government link, which moved here from the platform on
// 2026-08-23. The repository was Level 2 from the first commit precisely so
// that this would be a change to one file rather than a migration of the
// deployment, and it was.
//
// Modules go in the Options.Modules callback below and nowhere else. Logic
// written in this file instead of in a module is logic no other deployment can
// have and no test can reach — the ecosystem strategy, §5 rule 3.
package main

import (
	"log/slog"
	"os"

	"github.com/gerege-systems/client-gerege-nexus/modules/egov"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/platform"
)

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
			egov.New(p)
		},
	})
	if err != nil {
		slog.Error("gerege client stopped", "error", err)
		os.Exit(1)
	}
}
