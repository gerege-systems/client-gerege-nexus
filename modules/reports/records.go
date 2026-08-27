/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package reports

import (
	"context"
	"errors"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// records is domain/reports.Store over the platform's two record contracts.
//
// The rows were this app's to write until 2026-08-23 — backend/domain/reports/
// postgres held the statements — and they are the platform's now, for the
// reason written beside the contracts: a schedule is mailed by the platform's
// sweep with nobody present, and a grant is what lets one organisation's report
// read another's rows. An app that wrote them with SQL of its own was an app
// that could not leave this repository.
//
// What this layer does is translate, including the refusals: the contract's
// sentinels become the domain's, so the rules above keep answering in their own
// vocabulary and nothing above them knows there is a contract at all.
type records struct {
	schedules nexus.ReportSchedules
	grants    nexus.ReportGrants
}

func (r records) CreateSchedule(ctx context.Context, tenantID string, schedule domain.Schedule) (string, error) {
	return r.schedules.Create(ctx, tenantID, asSchedule(schedule))
}

func (r records) UpdateSchedule(ctx context.Context, tenantID, id string, schedule domain.Schedule) (bool, error) {
	return r.schedules.Update(ctx, tenantID, id, asSchedule(schedule))
}

func (r records) DeleteSchedule(ctx context.Context, tenantID, id string) (string, error) {
	reportKey, err := r.schedules.Delete(ctx, tenantID, id)
	if errors.Is(err, nexus.ErrReportScheduleNotFound) {
		return "", domain.ErrNoSuchSchedule
	}
	return reportKey, err
}

func (r records) TenantByRegistration(ctx context.Context, registration string) (string, error) {
	tenantID, err := r.grants.OrganisationByRegistration(ctx, registration)
	if errors.Is(err, nexus.ErrOrganisationNotFound) {
		return "", domain.ErrNoSuchTenant
	}
	return tenantID, err
}

func (r records) RegistrationOf(ctx context.Context, tenantID string) (string, error) {
	return r.grants.RegistrationOf(ctx, tenantID)
}

func (r records) CreateGrant(ctx context.Context, grant domain.Grant) (string, error) {
	id, err := r.grants.Request(ctx, nexus.ReportGrant{
		ReportKey:          grant.ReportKey,
		GrantorWorkspaceID: grant.GrantorWorkspaceID,
		GranteeWorkspaceID: grant.GranteeWorkspaceID,
		Scope:              grant.Scope,
		CounterpartyRef:    grant.CounterpartyRef,
		ValidUntil:         grant.ValidUntil,
		Note:               grant.Note,
		CreatedBy:          grant.CreatedBy,
	})
	if errors.Is(err, nexus.ErrReportGrantExists) {
		return "", domain.ErrGrantExists
	}
	return id, err
}

func (r records) AcceptGrant(ctx context.Context, grantorTenantID, id, actorUserID string) (string, error) {
	reportKey, err := r.grants.Accept(ctx, grantorTenantID, id, actorUserID)
	if errors.Is(err, nexus.ErrReportGrantNotPending) {
		return "", domain.ErrNoPendingRequest
	}
	return reportKey, err
}

func (r records) RevokeGrant(ctx context.Context, tenantID, id string) (string, string, error) {
	reportKey, side, err := r.grants.Revoke(ctx, tenantID, id)
	if errors.Is(err, nexus.ErrReportGrantNotFound) {
		return "", "", domain.ErrNoSuchGrant
	}
	return reportKey, side, err
}

// asSchedule is the domain's schedule as the platform stores it.
//
// The domain has already decided everything that makes it valid — the cron
// parses, the format is one this deployment renders, the recipients are
// addresses — so this carries the fields across and nothing else.
func asSchedule(schedule domain.Schedule) nexus.ReportSchedule {
	return nexus.ReportSchedule{
		ReportKey:  schedule.ReportKey,
		Name:       schedule.Name,
		Params:     schedule.Params,
		Cron:       schedule.Cron,
		Format:     schedule.Format,
		Recipients: schedule.Recipients,
		Active:     schedule.Active,
		CreatedBy:  schedule.CreatedBy,
	}
}

var _ domain.Store = records{}
