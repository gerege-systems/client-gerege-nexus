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
// It compiles no modules of its own yet. The repository is Level 2 from the
// first commit so that adding the first module is a change to this file rather
// than a migration of the deployment — go.mod, the image, the catalogue and
// the stack are already in the shape a module needs.
//
// Modules go in the Options.Modules callback below and nowhere else. Logic
// written in this file instead of in a module is logic no other deployment can
// have and no test can reach — the ecosystem strategy, §5 rule 3.
package main

import (
	"log/slog"
	"os"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/platform"
)

func main() {
	// The error is checked and the exit code is the point: a distribution that
	// cannot start — a catalogue that disagrees with its modules, a database it
	// cannot reach — must not exit 0 and read as a clean shutdown to whatever
	// is supervising it.
	err := platform.Run(platform.Options{
		// Empty on purpose, not forgotten. Registering nothing here leaves the
		// platform's own apps — organisation, egov, documents, reports,
		// sso-clients, urtuu — which is exactly what this deployment offers
		// today.
		Modules: func(p nexus.Platform) {},
	})
	if err != nil {
		slog.Error("gerege client stopped", "error", err)
		os.Exit(1)
	}
}
