/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package egov

import "context"

// Registry is the state's registers, as this app asks them.
//
// It is also what this app offers the rest of the binary: a module that wants
// to fill an address book from the citizen register holds one of these or holds
// nil, and nil is a feature it does not offer rather than an error it reports.
// The types are this package's rather than the XYP client's for that reason —
// a consumer should not have to import a platform package to name what it got.
type Registry interface {
	Citizen(ctx context.Context, regNumber string) (Citizen, error)
	Company(ctx context.Context, companyReg string) (Company, error)
}

// Rails is what this deployment is wired to. A function rather than a snapshot:
// the answer is read from the process's configuration, and reading it per
// request keeps the screen honest if that ever stops being fixed at boot.
type Rails func() []Rail

// History is what this organisation has asked the state.
//
// It reads the audit trail rather than a table of this app's own: the lookups
// already write an audit event, a second record of the same act would be a
// second thing to keep in step, and the audit table is the one place a deletion
// is not expected — an organisation should not be able to tidy away the record
// of whose registry data it read.
type History interface {
	Lookups(ctx context.Context, tenantID string) ([]Lookup, error)
}
