/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package reports

import "context"

// Catalogue is the reporting engine as the rules here need it.
//
// Not the engine: nothing in this package runs a report, exports one or mails
// one. What the rules ask is narrower and answerable without any of that —
// does this report exist, may it be shared that way, would the engine accept
// these parameters, is this a schedule expression. The adapter answers with
// internal/platform/reporting; a test answers with a map.
//
// The three Validate methods forward the engine's own message on refusal. The
// caller has been reading those sentences since before this package existed,
// and restating them in worse words here would be a change nobody asked for.
type Catalogue interface {
	Report(key string) (Report, bool)
	// ForApps is the app gate applied to the whole catalogue.
	ForApps(installed map[string]bool) []Report
	// Title resolves a report's name in the caller's language, falling back the
	// way the engine falls back.
	Title(report Report, locale string) string
	ValidateParams(key string, params map[string]string, locale string) error
	ValidateCron(expression string) error
	// NormalizeFormat answers with the format the engine will store, so that
	// "XLSX" and "xlsx" do not become two different rows.
	NormalizeFormat(raw string) (string, error)
}

// Installations answers which apps an organisation has.
//
// Supplied rather than queried, because the platform already caches that answer
// and this app must not become a second, differently-stale source of it.
type Installations func(ctx context.Context, tenantID string) (map[string]bool, error)

// Store is the rows this app owns: its schedules and its sharing agreements.
//
// Reading a list of either is deliberately absent. The schedules table is also
// the scheduler's, the grants table is also the consolidated engine's, and both
// already have readers in the platform that this app calls straight through —
// a second reader here would be a second shape for the same row.
type Store interface {
	CreateSchedule(ctx context.Context, tenantID string, schedule Schedule) (string, error)
	// UpdateSchedule reports whether the schedule was this organisation's to
	// update.
	UpdateSchedule(ctx context.Context, tenantID, id string, schedule Schedule) (bool, error)
	// DeleteSchedule answers with the report the schedule named, for the audit
	// entry, or ErrNoSuchSchedule.
	DeleteSchedule(ctx context.Context, tenantID, id string) (string, error)

	// TenantByRegistration deliberately looks outside the caller's own
	// organisation, and narrowly: an exact registration number in, an id or
	// ErrNoSuchTenant out.
	TenantByRegistration(ctx context.Context, registration string) (string, error)
	RegistrationOf(ctx context.Context, tenantID string) (string, error)

	// CreateGrant answers ErrGrantExists when one live agreement already
	// covers this pair and report — a partial unique index, not a check.
	CreateGrant(ctx context.Context, grant Grant) (string, error)
	// AcceptGrant is the grantor agreeing, and the tenant it takes is the whole
	// authorization: both parties can see the row, so a query without it would
	// let a grantee accept their own request. It answers with the report key,
	// or ErrNoPendingRequest.
	AcceptGrant(ctx context.Context, grantorTenantID, id, actorUserID string) (string, error)
	// RevokeGrant may be either party. It answers with the report key and which
	// side ended it, or ErrNoSuchGrant.
	RevokeGrant(ctx context.Context, tenantID, id string) (reportKey, side string, err error)
}
