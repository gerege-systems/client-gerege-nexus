/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package egov

import (
	"context"
	"strings"
)

type Service struct {
	registry Registry
	rails    Rails
	history  History
}

func NewService(registry Registry, rails Rails, history History) *Service {
	return &Service{registry: registry, rails: rails, history: history}
}

// Citizen looks a person up in the national registry.
//
// The number is required and is not otherwise checked here: what a valid
// registration number looks like is the state's rule, it has changed, and a
// second opinion about it in this package would refuse numbers the register
// itself accepts.
func (s *Service) Citizen(ctx context.Context, regNumber string) (Citizen, error) {
	regNumber = strings.TrimSpace(regNumber)
	if regNumber == "" {
		return Citizen{}, ErrNoRegNumber
	}
	citizen, err := s.registry.Citizen(ctx, regNumber)
	if err != nil {
		return Citizen{}, LookupFailed("citizen", err)
	}
	return citizen, nil
}

func (s *Service) Company(ctx context.Context, companyReg string) (Company, error) {
	companyReg = strings.TrimSpace(companyReg)
	if companyReg == "" {
		return Company{}, ErrNoCompanyReg
	}
	company, err := s.registry.Company(ctx, companyReg)
	if err != nil {
		return Company{}, LookupFailed("company", err)
	}
	return company, nil
}

// Connections is what this deployment is wired to.
//
// A deployment wired to nothing answers with an empty list rather than with
// nothing at all: "no rails" is the true answer for most installations of this
// platform, and a screen that cannot say it would have to guess.
func (s *Service) Connections() Connections {
	rails := []Rail{}
	if s.rails != nil {
		rails = s.rails()
	}
	return Connections{Rails: rails, IdentitiesPath: IdentitiesPath}
}

func (s *Service) History(ctx context.Context, tenantID string) ([]Lookup, error) {
	lookups, err := s.history.Lookups(ctx, tenantID)
	if err != nil {
		return nil, Failed("could not load the history", err)
	}
	return lookups, nil
}
