/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * NOT FOR PRODUCTION. A registry that answers from a map and a history kept in
 * one process's memory, so the app's rules can be run without ХУР and without
 * PostgreSQL. Nothing else may import it.
 */
package memory

import (
	"context"
	"errors"

	"github.com/gerege-systems/client-gerege-nexus/domain/egov"
)

// Registry answers what it was given, and refuses everything else the way an
// unreachable rail does.
type Registry struct {
	Citizens  map[string]egov.Citizen
	Companies map[string]egov.Company
	// Err, when set, is what every lookup fails with — a rail that is down
	// rather than a subject that is missing.
	Err error
}

func (r Registry) Citizen(_ context.Context, regNumber string) (egov.Citizen, error) {
	if r.Err != nil {
		return egov.Citizen{}, r.Err
	}
	citizen, found := r.Citizens[regNumber]
	if !found {
		return egov.Citizen{}, errors.New("no such citizen")
	}
	return citizen, nil
}

func (r Registry) Company(_ context.Context, companyReg string) (egov.Company, error) {
	if r.Err != nil {
		return egov.Company{}, r.Err
	}
	company, found := r.Companies[companyReg]
	if !found {
		return egov.Company{}, errors.New("no such company")
	}
	return company, nil
}

// History is the audit trail as a slice, keyed by organisation.
type History struct {
	ByTenant map[string][]egov.Lookup
	Err      error
}

func (h History) Lookups(_ context.Context, tenantID string) ([]egov.Lookup, error) {
	if h.Err != nil {
		return nil, h.Err
	}
	return h.ByTenant[tenantID], nil
}

var (
	_ egov.Registry = Registry{}
	_ egov.History  = History{}
)
