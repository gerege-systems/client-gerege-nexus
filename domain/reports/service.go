/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package reports

import (
	"context"
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"
)

// maxRecipients bounds an address list. Twenty is past every real distribution
// list and short of turning a schedule into a mailing service.
const maxRecipients = 20

type Service struct {
	catalogue Catalogue
	store     Store
	installed Installations
}

func NewService(catalogue Catalogue, store Store, installed Installations) *Service {
	return &Service{catalogue: catalogue, store: store, installed: installed}
}

// Available is the list: the reports of the apps this organisation has,
// grouped by app and ordered so the screen is the same on every replica.
func (s *Service) Available(ctx context.Context, tenantID, locale string) ([]Group, error) {
	installed, err := s.installed(ctx, tenantID)
	if err != nil {
		return nil, Failed("could not check the installed apps", err)
	}

	byApp := map[string][]Summary{}
	for _, report := range s.catalogue.ForApps(installed) {
		byApp[report.App] = append(byApp[report.App], Summary{
			Key:    report.Key,
			App:    report.App,
			Title:  s.catalogue.Title(report, locale),
			Titles: report.Titles,
		})
	}

	apps := make([]string, 0, len(byApp))
	for app := range byApp {
		apps = append(apps, app)
	}
	sort.Strings(apps)

	groups := make([]Group, 0, len(apps))
	for _, app := range apps {
		groups = append(groups, Group{App: app, Reports: byApp[app]})
	}
	return groups, nil
}

// Resolve is the guard every report endpoint shares: a report that exists, and
// an app this organisation has installed.
//
// The second check is the one that matters and the easiest to leave out.
// Without it, a caller who knows a key runs a report belonging to an app their
// organisation never installed — the list would not show it and the API would
// serve it anyway.
func (s *Service) Resolve(ctx context.Context, tenantID, key string) (Report, error) {
	report, err := s.visible(ctx, tenantID, key)
	if err != nil {
		// A report named in the path that this organisation cannot see is a
		// 404, not the 400 the same words mean in a body.
		if errors.Is(err, ErrNoSuchReport) {
			return Report{}, ErrReportUnavailable
		}
		return Report{}, err
	}
	return report, nil
}

// visible is Resolve without the status distinction: it answers ErrNoSuchReport
// whether the key is unknown or belongs to an app this organisation does not
// have, because telling those apart would enumerate the catalogue.
func (s *Service) visible(ctx context.Context, tenantID, key string) (Report, error) {
	report, found := s.catalogue.Report(strings.TrimSpace(key))
	if !found {
		return Report{}, ErrNoSuchReport
	}
	installed, err := s.installed(ctx, tenantID)
	if err != nil {
		return Report{}, Failed("could not check the installed apps", err)
	}
	if !installed[report.App] {
		return Report{}, ErrNoSuchReport
	}
	return report, nil
}

// --- schedules ---------------------------------------------------------------

// CreateSchedule stores a schedule that will run without anybody present.
//
// Everything is checked now rather than at the first run: a schedule with an
// unparseable expression or an address nobody can receive at is one that fails
// silently at three in the morning, weeks after whoever created it has stopped
// watching.
func (s *Service) CreateSchedule(ctx context.Context, tenantID string, edit ScheduleEdit) (Schedule, error) {
	schedule, err := s.check(ctx, tenantID, edit)
	if err != nil {
		return Schedule{}, err
	}
	id, err := s.store.CreateSchedule(ctx, tenantID, schedule)
	if err != nil {
		return Schedule{}, Failed("could not save the schedule", err)
	}
	schedule.ID = id
	return schedule, nil
}

func (s *Service) UpdateSchedule(ctx context.Context, tenantID, id string, edit ScheduleEdit) (Schedule, error) {
	schedule, err := s.check(ctx, tenantID, edit)
	if err != nil {
		return Schedule{}, err
	}
	found, err := s.store.UpdateSchedule(ctx, tenantID, id, schedule)
	if err != nil {
		return Schedule{}, Failed("could not update the schedule", err)
	}
	if !found {
		return Schedule{}, ErrNoSuchSchedule
	}
	schedule.ID = id
	return schedule, nil
}

// DeleteSchedule answers with the report the schedule named, which is what the
// audit entry records.
func (s *Service) DeleteSchedule(ctx context.Context, tenantID, id string) (string, error) {
	reportKey, err := s.store.DeleteSchedule(ctx, tenantID, id)
	if err != nil {
		return "", Failed("could not remove the schedule", err)
	}
	return reportKey, nil
}

// check is everything a schedule has to satisfy, in the order the caller is
// told about it.
func (s *Service) check(ctx context.Context, tenantID string, edit ScheduleEdit) (Schedule, error) {
	report, err := s.visible(ctx, tenantID, edit.ReportKey)
	if err != nil {
		return Schedule{}, err
	}

	if err := s.catalogue.ValidateCron(edit.Cron); err != nil {
		return Schedule{}, Refused("the schedule expression is not valid: " + err.Error())
	}
	format, err := s.catalogue.NormalizeFormat(edit.Format)
	if err != nil {
		return Schedule{}, Refused(err.Error())
	}

	// The parameters have to be ones the report accepts, now. A schedule is
	// stored and run later, so this is the last moment anybody is present to be
	// told they are wrong. The locale is fixed: what is being asked is whether
	// the values bind, and nobody is reading the answer.
	params := edit.Params
	if params == nil {
		params = map[string]string{}
	}
	if err := s.catalogue.ValidateParams(report.Key, params, "mn"); err != nil {
		return Schedule{}, Refused(err.Error())
	}

	recipients, err := normalizeRecipients(edit.Recipients)
	if err != nil {
		return Schedule{}, err
	}

	return Schedule{
		ReportKey:  edit.ReportKey,
		Name:       trimmed(edit.Name),
		Params:     params,
		Cron:       edit.Cron,
		Format:     format,
		Recipients: recipients,
		Active:     edit.Active == nil || *edit.Active,
		CreatedBy:  actorFrom(ctx),
	}, nil
}

// normalizeRecipients is the address list as it is stored: lowercased, without
// duplicates, and every entry one a mail server would accept.
func normalizeRecipients(given []string) ([]string, error) {
	if len(given) == 0 {
		return nil, ErrNoRecipients
	}
	if len(given) > maxRecipients {
		return nil, ErrTooManyPeople
	}

	seen := make(map[string]bool, len(given))
	cleaned := make([]string, 0, len(given))
	for _, raw := range given {
		address, err := mail.ParseAddress(trimmed(raw))
		if err != nil {
			return nil, NotAnAddress(trimmed(raw))
		}
		lowered := strings.ToLower(address.Address)
		if seen[lowered] {
			continue
		}
		seen[lowered] = true
		cleaned = append(cleaned, lowered)
	}
	return cleaned, nil
}

// --- sharing -----------------------------------------------------------------

// RequestGrant is the grantee asking to be shown a report.
//
// It creates a request, not a permission: the owning organisation's
// administrator has to accept before anything is readable, which is §3.5's
// second principle — the data owner decides.
func (s *Service) RequestGrant(ctx context.Context, granteeTenantID string, request GrantRequest) (Grant, error) {
	report, found := s.catalogue.Report(trimmed(request.ReportKey))
	if !found {
		return Grant{}, ErrNoSuchReport
	}

	scope := trimmed(request.Scope)
	if scope == "" {
		scope = ScopeCounterparty
	}
	if scope != ScopeCounterparty && scope != ScopeFull {
		return Grant{}, ErrInvalidScope
	}
	// Default deny: a report that was not written to be shared cannot be, and
	// one that cannot filter by counterparty cannot be granted that scope —
	// rather than the scope quietly widening to everything.
	if !report.Supports(scope) {
		return Grant{}, ErrUnshareable
	}

	grantorTenantID, err := s.store.TenantByRegistration(ctx, trimmed(request.GrantorRegistrationNumber))
	if err != nil {
		// Anything that is not an answer is "no such organisation": this is a
		// lookup across the tenant boundary and must not report anything else
		// about what it found.
		return Grant{}, ErrNoSuchTenant
	}
	if grantorTenantID == granteeTenantID {
		return Grant{}, ErrSelfRequest
	}

	counterpartyRef := ""
	if scope == ScopeCounterparty {
		counterpartyRef, err = s.store.RegistrationOf(ctx, granteeTenantID)
		if err != nil || counterpartyRef == "" {
			return Grant{}, ErrNoRegistration
		}
	}

	var validUntil *time.Time
	if raw := trimmed(request.ValidUntil); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return Grant{}, ErrInvalidValidTo
		}
		validUntil = &parsed
	}

	grant := Grant{
		GrantorWorkspaceID: grantorTenantID,
		GranteeWorkspaceID: granteeTenantID,
		ReportKey:          report.Key,
		Scope:              scope,
		CounterpartyRef:    counterpartyRef,
		ValidUntil:         validUntil,
		Note:               trimmed(request.Note),
		CreatedBy:          actorFrom(ctx),
	}
	id, err := s.store.CreateGrant(ctx, grant)
	if err != nil {
		return Grant{}, Failed("could not record the request", err)
	}
	grant.ID = id
	return grant, nil
}

// AcceptGrant is the grantor agreeing. Only they can.
func (s *Service) AcceptGrant(ctx context.Context, grantorTenantID, id string) (string, error) {
	reportKey, err := s.store.AcceptGrant(ctx, grantorTenantID, id, actorFrom(ctx))
	if err != nil {
		return "", Failed("could not accept the request", err)
	}
	return reportKey, nil
}

// RevokeGrant ends an agreement. Either side may: the owner revoking is the
// point — the permission is revocable and takes effect immediately — and the
// reader withdrawing is a request they no longer want, which nobody benefits
// from leaving open.
//
// The row is not deleted. "Who could see our data, and when" is a question the
// owner is entitled to an answer to after the fact.
func (s *Service) RevokeGrant(ctx context.Context, tenantID, id string) (string, string, error) {
	reportKey, side, err := s.store.RevokeGrant(ctx, tenantID, id)
	if err != nil {
		return "", "", Failed("could not revoke the agreement", err)
	}
	return reportKey, side, nil
}

// --- the acting person --------------------------------------------------------

type actorKey struct{}

// WithActor names the person the request is being made by. The platform knows
// who that is; this package only needs to write it down, on a grant row and in
// nothing else.
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

func actorFrom(ctx context.Context) string {
	userID, _ := ctx.Value(actorKey{}).(string)
	return userID
}
