/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * NOT FOR PRODUCTION. A catalogue and a store kept in one process's memory, so
 * that the rules next door can be run without PostgreSQL and without the
 * reporting engine.
 *
 * Nothing else may import it. It enforces none of the schema's constraints
 * beyond the two the rules above it ask about — one live agreement per pair,
 * and a schedule belonging to the organisation editing it.
 */
package memory

import (
	"context"
	"strconv"
	"sync"

	"github.com/gerege-systems/client-gerege-nexus/domain/reports"
)

// Catalogue stands in for the reporting engine.
//
// The three Validate answers are fields rather than logic: what the rules do
// with them is refuse and forward the message, and re-implementing a cron
// parser here would be testing the fake.
type Catalogue struct {
	Reports     map[string]reports.Report
	CronError   error
	FormatError error
	ParamsError error
	// Format is what NormalizeFormat answers with when it does not refuse.
	Format string
}

func (c Catalogue) Report(key string) (reports.Report, bool) {
	report, found := c.Reports[key]
	return report, found
}

func (c Catalogue) ForApps(installed map[string]bool) []reports.Report {
	permitted := make([]reports.Report, 0, len(c.Reports))
	for _, report := range c.Reports {
		if installed[report.App] {
			permitted = append(permitted, report)
		}
	}
	return permitted
}

func (c Catalogue) Title(report reports.Report, locale string) string {
	if title := report.Titles[locale]; title != "" {
		return title
	}
	if title := report.Titles["mn"]; title != "" {
		return title
	}
	return report.Key
}

func (c Catalogue) ValidateParams(string, map[string]string, string) error { return c.ParamsError }

func (c Catalogue) ValidateCron(string) error { return c.CronError }

func (c Catalogue) NormalizeFormat(raw string) (string, error) {
	if c.FormatError != nil {
		return "", c.FormatError
	}
	if c.Format != "" {
		return c.Format, nil
	}
	return raw, nil
}

// Store keeps the two tables this app owns.
type Store struct {
	mu            sync.Mutex
	schedules     []storedSchedule
	grants        []storedGrant
	registrations map[string]string // tenant id → registration number
	nextID        int
}

type storedSchedule struct {
	reports.Schedule
	tenantID string
}

type storedGrant struct {
	reports.Grant
	accepted bool
	revoked  bool
}

func New() *Store { return &Store{registrations: map[string]string{}} }

// Register gives an organisation a registration number, which is what a
// sharing request names the other side by.
func (s *Store) Register(tenantID, registration string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registrations[tenantID] = registration
}

func (s *Store) id() string {
	s.nextID++
	return strconv.Itoa(s.nextID)
}

func (s *Store) CreateSchedule(_ context.Context, tenantID string, schedule reports.Schedule) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedule.ID = s.id()
	s.schedules = append(s.schedules, storedSchedule{Schedule: schedule, tenantID: tenantID})
	return schedule.ID, nil
}

func (s *Store) UpdateSchedule(_ context.Context, tenantID, id string, schedule reports.Schedule) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stored := range s.schedules {
		if stored.ID == id && stored.tenantID == tenantID {
			schedule.ID = id
			s.schedules[i] = storedSchedule{Schedule: schedule, tenantID: tenantID}
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) DeleteSchedule(_ context.Context, tenantID, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stored := range s.schedules {
		if stored.ID == id && stored.tenantID == tenantID {
			s.schedules = append(s.schedules[:i], s.schedules[i+1:]...)
			return stored.ReportKey, nil
		}
	}
	return "", reports.ErrNoSuchSchedule
}

// Schedule is what the tests read back. Production has no such method: the
// schedules screen reads through the platform's own lister.
func (s *Store) Schedule(id string) (reports.Schedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, stored := range s.schedules {
		if stored.ID == id {
			return stored.Schedule, true
		}
	}
	return reports.Schedule{}, false
}

func (s *Store) TenantByRegistration(_ context.Context, registration string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tenantID, known := range s.registrations {
		if known != "" && known == registration {
			return tenantID, nil
		}
	}
	return "", reports.ErrNoSuchTenant
}

func (s *Store) RegistrationOf(_ context.Context, tenantID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registrations[tenantID], nil
}

func (s *Store) CreateGrant(_ context.Context, grant reports.Grant) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The partial unique index: one live agreement per pair per report.
	for _, stored := range s.grants {
		if stored.revoked {
			continue
		}
		if stored.GrantorWorkspaceID == grant.GrantorWorkspaceID &&
			stored.GranteeWorkspaceID == grant.GranteeWorkspaceID &&
			stored.ReportKey == grant.ReportKey {
			return "", reports.ErrGrantExists
		}
	}
	grant.ID = s.id()
	s.grants = append(s.grants, storedGrant{Grant: grant})
	return grant.ID, nil
}

func (s *Store) AcceptGrant(_ context.Context, grantorTenantID, id, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stored := range s.grants {
		if stored.ID != id || stored.GrantorWorkspaceID != grantorTenantID {
			continue
		}
		if stored.accepted || stored.revoked {
			break
		}
		s.grants[i].accepted = true
		return stored.ReportKey, nil
	}
	return "", reports.ErrNoPendingRequest
}

func (s *Store) RevokeGrant(_ context.Context, tenantID, id string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, stored := range s.grants {
		if stored.ID != id || stored.revoked {
			continue
		}
		switch tenantID {
		case stored.GrantorWorkspaceID:
			s.grants[i].revoked = true
			return stored.ReportKey, reports.SideGiven, nil
		case stored.GranteeWorkspaceID:
			s.grants[i].revoked = true
			return stored.ReportKey, reports.SideReceived, nil
		}
	}
	return "", "", reports.ErrNoSuchGrant
}

var (
	_ reports.Store     = (*Store)(nil)
	_ reports.Catalogue = Catalogue{}
)
