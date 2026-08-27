/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package reports is the app around the reporting engine, without the engine.
 *
 * The distinction is the whole of why this package is small. Running a report,
 * exporting it, binding its parameters and mailing it on a timer are the
 * platform's — internal/platform/reporting — and they stay there: they are one
 * engine every distribution's modules declare reports against, not a rule of
 * this app. What is this app's is everything around them: which reports an
 * organisation may see at all, what a schedule has to satisfy before it is
 * stored, and who may agree to show another organisation a report.
 *
 * Those were HTTP handlers, and the sharing rules in particular were reachable
 * only by making a request with two tenants set up. They are the rules a person
 * would say out loud about this app, so they are here, in stdlib Go.
 */
package reports

import (
	"strings"
	"time"
)

// Report is what this app needs to know about one report. The engine's own
// Report is an interface with a Run on it; none of the rules here run anything.
type Report struct {
	Key    string
	App    string
	Titles map[string]string
	// Scopes are the sharing scopes this report can honour. Empty means it was
	// not written to be shared, and no grant may name it.
	Scopes []string
}

// Supports reports whether this report may be granted with the given scope.
func (r Report) Supports(scope string) bool {
	for _, supported := range r.Scopes {
		if supported == scope {
			return true
		}
	}
	return false
}

// The two sharing scopes. Counterparty is the contracted-parties case — a mine
// seeing the transport company's trips for its own cargo; full is the
// hierarchical one, a parent organisation consolidating its subsidiaries.
const (
	ScopeCounterparty = "counterparty"
	ScopeFull         = "full"
)

// Group is the list as the screen reads it: the reports of one app.
//
// Grouped rather than flat because the grouping is the app gate made visible —
// a tenant sees the sections for the apps it has, and one it does not have
// simply is not there, rather than being there and refusing when clicked.
type Group struct {
	App     string    `json:"app"`
	Reports []Summary `json:"reports"`
}

// Summary is one entry of the list.
type Summary struct {
	Key    string            `json:"key"`
	App    string            `json:"app"`
	Title  string            `json:"title"`
	Titles map[string]string `json:"titles"`
}

// ScheduleEdit is a schedule as somebody has just described it, before any of
// it has been checked.
type ScheduleEdit struct {
	ReportKey  string            `json:"report_key"`
	Name       string            `json:"name"`
	Params     map[string]string `json:"params"`
	Cron       string            `json:"cron"`
	Format     string            `json:"format"`
	Recipients []string          `json:"recipients"`
	// Active absent means active. A schedule created without the field is one
	// somebody meant to start.
	Active *bool `json:"active"`
}

// Schedule is a schedule that has passed every check, as it is stored.
type Schedule struct {
	ID         string
	ReportKey  string
	Name       string
	Params     map[string]string
	Cron       string
	Format     string
	Recipients []string
	Active     bool
	// CreatedBy is who set it up, which the row keeps so that a schedule
	// arriving in somebody's inbox at six in the morning has an author.
	CreatedBy string
}

// GrantRequest is one organisation asking to be shown another's report.
type GrantRequest struct {
	// Whose data is being asked for, by registration number rather than by
	// tenant id. A tenant id is an internal identifier a requester has no
	// legitimate way to know, and letting one be typed in would turn this form
	// into a way to enumerate the organisations on the deployment.
	GrantorRegistrationNumber string `json:"grantor_registration_number"`
	ReportKey                 string `json:"report_key"`
	Scope                     string `json:"scope"`
	ValidUntil                string `json:"valid_until"`
	Note                      string `json:"note"`
}

// Grant is a request that has passed every check, as it is stored: a request
// and not yet a permission, because accepted_at is null until the owning
// organisation answers.
type Grant struct {
	ID                 string
	GrantorWorkspaceID string
	GranteeWorkspaceID string
	ReportKey          string
	Scope              string
	// CounterpartyRef is decided once, here, and stored. Matching it again on
	// every run would mean a grant silently pointing at different data after
	// the requesting organisation edited its own profile.
	CounterpartyRef string
	ValidUntil      *time.Time
	Note            string
	CreatedBy       string
}

// The two sides of an agreement, for the audit entry a revoke writes.
const (
	SideGiven    = "given"
	SideReceived = "received"
)

func trimmed(value string) string { return strings.TrimSpace(value) }
