/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package reports_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The platform, in memory.
//
// These tests ran against internal/platform/reporting over a migrated Postgres
// while this app lived in the core: a real engine, a real scheduler's tables, a
// real grants table. None of that could come here and none of it should have —
// the engine is the platform's to test, and what is left in this app after the
// contracts landed is its own half: which reports a tenant may see, what a
// schedule body has to satisfy, and who may accept or end a sharing agreement.
//
// So the three contracts are three small fakes, and the assertions that used to
// read a row with SQL read the fake's map instead. The engine fake answers
// enough to exercise the app and no more: a report either exists or it does
// not, a run produces one row, an export produces bytes.

// everyPermission stands in for the RBAC store: what the routes are gated on is
// declared by the module and asserted in the core's own access-policy test.
type everyPermission struct{}

func (everyPermission) GetUserPermissions(context.Context, string, string) (map[string]bool, error) {
	return map[string]bool{
		"reports.view": true, "reports.schedule": true, "reports.share": true,
	}, nil
}

// fakeEngine is nexus.ReportEngine over a fixed list of reports.
type fakeEngine struct{ reports []nexus.ReportDescription }

func (e fakeEngine) Available(installed map[string]bool) []nexus.ReportDescription {
	available := make([]nexus.ReportDescription, 0, len(e.reports))
	for _, report := range e.reports {
		if installed == nil || installed[report.App] {
			available = append(available, report)
		}
	}
	return available
}

func (e fakeEngine) Describe(key string) (nexus.ReportDescription, bool) {
	for _, report := range e.reports {
		if report.Key == key {
			return report, true
		}
	}
	return nexus.ReportDescription{}, false
}

func (e fakeEngine) Form(_ context.Context, _, key, _ string) (*nexus.ReportForm, error) {
	described, found := e.Describe(key)
	if !found {
		return nil, errors.New("no such report")
	}
	return &nexus.ReportForm{Key: described.Key, App: described.App, Titles: described.Titles}, nil
}

func (e fakeEngine) Run(_ context.Context, _, key string, _ map[string]string, _ string) (*nexus.ReportRun, error) {
	described, found := e.Describe(key)
	if !found {
		return nil, errors.New("no such report")
	}
	return &nexus.ReportRun{
		Key: described.Key, Title: described.Titles["en"],
		Result: nexus.Result{Rows: []map[string]any{{"label": "Нэг", "amount": 1}}},
	}, nil
}

func (e fakeEngine) RunConsolidated(ctx context.Context, tenantID, key string,
	params map[string]string, locale, _ string) (*nexus.ReportRun, error) {
	return e.Run(ctx, tenantID, key, params, locale)
}

func (e fakeEngine) Export(_ context.Context, _, key string, _ map[string]string, _, format string) (*nexus.ReportExport, error) {
	if _, found := e.Describe(key); !found {
		return nil, errors.New("no such report")
	}
	if err := validFormat(format); err != nil {
		return nil, err
	}
	return &nexus.ReportExport{
		Filename: key + "." + strings.ToLower(format), ContentType: "text/csv",
		Bytes: []byte("label,amount\nНэг,1\n"), Rows: 1,
	}, nil
}

func (e fakeEngine) ValidateSchedule(key string, _ map[string]string, _, cron, format string) error {
	if _, found := e.Describe(key); !found {
		return errors.New("no such report")
	}
	if err := e.ValidateCron(cron); err != nil {
		return err
	}
	return validFormat(format)
}

// ValidateCron is the grammar and nothing else: five fields. The real parser
// checks the ranges too, and what this app does with the answer is the same
// either way — refuse the body and say so.
func (fakeEngine) ValidateCron(expression string) error {
	if len(strings.Fields(expression)) != 5 {
		return errors.New("a cron expression has five fields")
	}
	return nil
}

func (fakeEngine) NormalizeFormat(raw string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(raw))
	if format == "" {
		format = "xlsx"
	}
	return format, validFormat(format)
}

func (fakeEngine) Deliverable() bool { return true }

func validFormat(raw string) error {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "xlsx", "csv":
		return nil
	default:
		return errors.New("unsupported format")
	}
}

// fakeSchedules is nexus.ReportSchedules as a map.
type fakeSchedules struct {
	mu    sync.Mutex
	byID  map[string]nexus.ReportSchedule
	owner map[string]string
}

func newFakeSchedules() *fakeSchedules {
	return &fakeSchedules{byID: map[string]nexus.ReportSchedule{}, owner: map[string]string{}}
}

func (s *fakeSchedules) List(_ context.Context, tenantID string) ([]nexus.ReportSchedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := make([]nexus.ReportSchedule, 0, len(s.byID))
	for id, schedule := range s.byID {
		if s.owner[id] == tenantID {
			found = append(found, schedule)
		}
	}
	return found, nil
}

func (s *fakeSchedules) Create(_ context.Context, tenantID string, schedule nexus.ReportSchedule) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	schedule.ID, schedule.CreatedAt = id, time.Now()
	s.byID[id], s.owner[id] = schedule, tenantID
	return id, nil
}

func (s *fakeSchedules) Update(_ context.Context, tenantID, id string, schedule nexus.ReportSchedule) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner[id] != tenantID {
		return false, nil
	}
	schedule.ID = id
	s.byID[id] = schedule
	return true, nil
}

func (s *fakeSchedules) Delete(_ context.Context, tenantID, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner[id] != tenantID {
		return "", nexus.ErrReportScheduleNotFound
	}
	reportKey := s.byID[id].ReportKey
	delete(s.byID, id)
	delete(s.owner, id)
	return reportKey, nil
}

// stored is what a test reads instead of the row it used to select.
func (s *fakeSchedules) stored(id string) (nexus.ReportSchedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule, found := s.byID[id]
	return schedule, found
}

// fakeGrants is nexus.ReportGrants as a map, with the two rules that matter
// kept: one live agreement per pair and report, and only the grantor accepts.
type fakeGrants struct {
	mu             sync.Mutex
	byID           map[string]nexus.ReportGrant
	byRegistration map[string]string
	registrationOf map[string]string
}

func newFakeGrants() *fakeGrants {
	return &fakeGrants{
		byID:           map[string]nexus.ReportGrant{},
		byRegistration: map[string]string{},
		registrationOf: map[string]string{},
	}
}

func (g *fakeGrants) organisation(tenantID, registration string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byRegistration[registration] = tenantID
	g.registrationOf[tenantID] = registration
}

func (g *fakeGrants) List(_ context.Context, tenantID string) ([]nexus.ReportGrant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	found := make([]nexus.ReportGrant, 0, len(g.byID))
	for _, grant := range g.byID {
		if grant.GrantorWorkspaceID == tenantID || grant.GranteeWorkspaceID == tenantID {
			found = append(found, grant)
		}
	}
	return found, nil
}

func (g *fakeGrants) History(context.Context, string) ([]nexus.ReportGrantUse, error) {
	return nil, nil
}

func (g *fakeGrants) Request(_ context.Context, grant nexus.ReportGrant) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, existing := range g.byID {
		live := existing.RevokedAt == nil
		if live && existing.ReportKey == grant.ReportKey &&
			existing.GrantorWorkspaceID == grant.GrantorWorkspaceID &&
			existing.GranteeWorkspaceID == grant.GranteeWorkspaceID {
			return "", nexus.ErrReportGrantExists
		}
	}
	id := uuid.NewString()
	grant.ID, grant.CreatedAt, grant.ValidFrom = id, time.Now(), time.Now()
	g.byID[id] = grant
	return id, nil
}

func (g *fakeGrants) Accept(_ context.Context, grantorTenantID, id, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant, found := g.byID[id]
	if !found || grant.GrantorWorkspaceID != grantorTenantID ||
		grant.RevokedAt != nil || grant.AcceptedAt != nil {
		return "", nexus.ErrReportGrantNotPending
	}
	accepted := time.Now()
	grant.AcceptedAt = &accepted
	g.byID[id] = grant
	return grant.ReportKey, nil
}

func (g *fakeGrants) Revoke(_ context.Context, tenantID, id string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant, found := g.byID[id]
	if !found || grant.RevokedAt != nil ||
		(grant.GrantorWorkspaceID != tenantID && grant.GranteeWorkspaceID != tenantID) {
		return "", "", nexus.ErrReportGrantNotFound
	}
	revoked := time.Now()
	grant.RevokedAt = &revoked
	g.byID[id] = grant
	side := "received"
	if grant.GrantorWorkspaceID == tenantID {
		side = "given"
	}
	return grant.ReportKey, side, nil
}

func (g *fakeGrants) OrganisationByRegistration(_ context.Context, registration string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tenantID, found := g.byRegistration[registration]
	if !found {
		return "", nexus.ErrOrganisationNotFound
	}
	return tenantID, nil
}

func (g *fakeGrants) RegistrationOf(_ context.Context, tenantID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.registrationOf[tenantID], nil
}

// stored is what a test reads instead of the row it used to select.
func (g *fakeGrants) stored(id string) (nexus.ReportGrant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	grant, found := g.byID[id]
	return grant, found
}
