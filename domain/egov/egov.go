/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package egov is this platform's front door to the Mongolian state's systems,
 * without the wires that reach them.
 *
 * The app is thin by design and this package says how thin: two lookups, a list
 * of what this deployment is wired to, and the record of what has been asked.
 * The rails themselves — ХУР, eID, ДАН — are the platform's, because they sign
 * people in before anybody has an app installed, and nothing here builds one.
 *
 * What is here that was not anywhere before: the three answers stated as values
 * rather than as handlers. A lookup needs a number, a lookup is worth keeping,
 * and a person's own identities are not this app's to manage — the last one is
 * a sentence the connections screen has to say out loud, because an app that
 * owned them would let an administrator take away somebody's ability to detach
 * their own national identity by uninstalling it.
 */
package egov

import "time"

// Citizen is a person as the national registry knows them.
//
// The JSON tags are the published shape of this app's answers, unchanged from
// when the struct lived in the platform's XYP client — the screen and every
// integration read these names.
type Citizen struct {
	RegNumber      string `json:"reg_number"`
	CivilID        string `json:"civil_id"`
	LastName       string `json:"last_name"`
	FirstName      string `json:"first_name"`
	Gender         string `json:"gender"`
	Address        string `json:"address"`
	PassportStatus string `json:"passport_status"`
	Verified       bool   `json:"verified"`
}

// Company is a legal entity as the state register knows it.
type Company struct {
	CompanyReg   string `json:"company_reg"`
	Name         string `json:"name"`
	Executive    string `json:"executive"`
	Address      string `json:"address"`
	VatPayer     bool   `json:"vat_payer"`
	Status       string `json:"status"`
	FoundingDate string `json:"founding_date"`
}

// Rail is one of the state's systems and what this deployment's connection to
// it is worth.
//
// Mode is "live", "mock" or "unconfigured": three states rather than a boolean,
// because a mock rail answers every question and none of the answers is
// authoritative — reporting that as "connected" is how a test fixture ends up
// on a government form.
type Rail struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Endpoint string `json:"endpoint,omitempty"`
}

// Lookup is one thing this organisation asked the state.
type Lookup struct {
	Action    string         `json:"action"`
	UserID    string         `json:"user_id"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

// Connections is the answer the connections screen needs: what this deployment
// is wired to, and where a person manages their own identity.
type Connections struct {
	Rails []Rail `json:"rails"`
	// IdentitiesPath is part of the answer rather than something the client
	// knows. This screen is the obvious place to look for "my eID", the control
	// for it is deliberately not here, and a screen that mentions the thing
	// without saying where it is only sends people looking through Settings.
	IdentitiesPath string `json:"identities_path"`
}

// IdentitiesPath is where a person's own linked identities live: the platform's
// profile screen, not this app.
const IdentitiesPath = "/profile"

// The audit actions a lookup writes. They are values because the history screen
// reads them back by name, and a rename in one place and not the other is a
// history that goes quiet.
const (
	ActionCitizenQueried = "egov.citizen_queried"
	ActionCompanyQueried = "egov.company_queried"
)
