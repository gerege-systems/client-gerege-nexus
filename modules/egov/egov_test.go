/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package egov_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gerege-systems/client-gerege-nexus/modules/egov"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// This module used to be tested against the platform's own implementations: a
// migrated Postgres, internal/platform/audit for the trail,
// internal/platform/gerege for the ХУР client and internal/platform/rbac for the
// permission store. All four are under internal/, so none of them could come
// here — which was, in the end, the thing that made the module unmovable.
//
// It is tested against the contracts now, with fakes. That is not a compromise
// forced by the move: it is a better test. What these assertions are about is
// what the module does — refuse a lookup to an ordinary member, record the one
// it allows, read its own history back, say when a rail is mocked. None of that
// is a fact about Postgres, and proving it through a database was proving the
// platform's audit table at the same time, in a test that could not say which
// of the two had broken.
//
// It needs no database at all now, which is the shape of the module rather than
// a trick of the test: egov holds nothing. It asks the state a question, and
// asks the platform to remember that it did.

// fakeRegistry answers like a ХУР in mock mode: a plausible record for anybody.
type fakeRegistry struct{ asked []string }

func (r *fakeRegistry) Citizen(_ context.Context, regNumber string) (*nexus.CitizenRecord, error) {
	r.asked = append(r.asked, regNumber)
	return &nexus.CitizenRecord{
		RegNumber: regNumber, CivilID: "МН" + regNumber,
		LastName: "Дорж", FirstName: "Бат", Gender: "M",
		Address: "Улаанбаатар", PassportStatus: "valid", Verified: true,
	}, nil
}

func (r *fakeRegistry) Company(_ context.Context, companyReg string) (*nexus.CompanyRecord, error) {
	r.asked = append(r.asked, companyReg)
	return &nexus.CompanyRecord{
		CompanyReg: companyReg, Name: "Тест ХХК", Executive: "Бат",
		Status: "active", VatPayer: true, FoundingDate: "2015-01-01",
	}, nil
}

// fakeAudit is both halves of the trail: what nexus.Audit writes and what
// nexus.AuditReader reads. One object, because the property under test is that
// the second sees what the first wrote.
type fakeAudit struct{ entries []nexus.AuditEntry }

func (a *fakeAudit) record(_ context.Context, _, userID, action, _ string, details map[string]any) {
	a.entries = append(a.entries, nexus.AuditEntry{
		Action: action, UserID: userID, Details: details, At: time.Now(),
	})
}

func (a *fakeAudit) RecentByPrefix(_ context.Context, _ string, prefixes []string, limit int) ([]nexus.AuditEntry, error) {
	matched := make([]nexus.AuditEntry, 0, len(a.entries))
	for i := len(a.entries) - 1; i >= 0 && len(matched) < limit; i-- {
		for _, prefix := range prefixes {
			if strings.HasPrefix(a.entries[i].Action, prefix+".") {
				matched = append(matched, a.entries[i])
				break
			}
		}
	}
	return matched, nil
}

// everyPermission stands in for the platform's store. The module's own gates
// are what this file is about; who holds a permission is Access control's
// business and is tested there.
type everyPermission struct{}

func (everyPermission) GetUserPermissions(context.Context, string, string) (map[string]bool, error) {
	return map[string]bool{"egov.read": true, "egov.citizen.read": true, "egov.company.read": true}, nil
}

type fixture struct {
	router   http.Handler
	registry *fakeRegistry
	audit    *fakeAudit
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	registry := &fakeRegistry{}
	trail := &fakeAudit{}
	rails := func() []nexus.StateRail {
		return []nexus.StateRail{{ID: "xyp", Name: "ХУР", Mode: "mock", Endpoint: "https://xyp.example.invalid"}}
	}

	nexus.Provide[nexus.StateRegistry](registry)
	nexus.Provide[nexus.StateRails](rails)
	nexus.Provide[nexus.AuditReader](trail)
	nexus.Provide[nexus.AuditSink](trail.record)
	t.Cleanup(func() {
		nexus.Provide[nexus.StateRegistry](nil)
		nexus.Provide[nexus.StateRails](nil)
		nexus.Provide[nexus.AuditReader](nil)
		nexus.Provide[nexus.AuditSink](nil)
	})

	module := egov.New(nexus.NewPlatform(nil, everyPermission{}))
	router := chi.NewRouter()
	module.RegisterRoutes(router, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := nexus.WithWorkspaceID(r.Context(), "11111111-1111-1111-1111-111111111111")
			ctx = nexus.WithUser(ctx, nexus.UserClaims{
				UserID:      "22222222-2222-2222-2222-222222222222",
				WorkspaceID: "11111111-1111-1111-1111-111111111111",
				IsAdmin:     true,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	return &fixture{router: router, registry: registry, audit: trail}
}

func (f *fixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// A registry lookup is a `.read` by grammar and an administrative act by
// consequence: it reads somebody's national record, not this organisation's
// rows. AdminOnly is what keeps the installer from handing it to every member.
func TestTheRegistryLookupsAreNotHandedToEveryMember(t *testing.T) {
	module := egov.New(nexus.NewPlatform(nil, nil))

	byCode := map[string]nexus.PermissionDefinition{}
	for _, permission := range module.Permissions() {
		byCode[permission.Code] = permission
	}

	for _, code := range []string{"egov.citizen.read", "egov.company.read"} {
		permission, declared := byCode[code]
		if !declared {
			t.Fatalf("%s is no longer declared by the module", code)
		}
		if !permission.AdminOnly {
			t.Errorf("%s is not AdminOnly: installing this app would grant it to every member", code)
		}
	}

	if byCode["egov.read"].AdminOnly {
		t.Error("egov.read is AdminOnly, so nobody but an administrator could open the app")
	}
}

// Asking is the thing that is kept. A lookup that left no trail would be a
// lookup nobody could account for.
func TestALookupAnswersAndIsRecorded(t *testing.T) {
	f := newFixture(t)

	res := f.do(t, http.MethodPost, "/api/v1/egov/citizen", `{"reg_number":"AA90010111"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("lookup answered %d: %s", res.Code, res.Body.String())
	}
	var info nexus.CitizenRecord
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.RegNumber != "AA90010111" {
		t.Fatalf("the reply is about somebody else: %+v", info)
	}
	if len(f.registry.asked) != 1 || f.registry.asked[0] != "AA90010111" {
		t.Fatalf("the register was asked %v", f.registry.asked)
	}

	res = f.do(t, http.MethodGet, "/api/v1/egov/history", "")
	if res.Code != http.StatusOK {
		t.Fatalf("history answered %d: %s", res.Code, res.Body.String())
	}
	var history []struct {
		Action  string         `json:"action"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Action != "egov.citizen_queried" {
		t.Fatalf("the history did not carry the lookup: %+v", history)
	}
	if history[0].Details["reg_number"] != "AA90010111" {
		t.Fatalf("the history entry names the wrong subject: %+v", history[0].Details)
	}
}

// The address the lookups had while they were platform routes still answers.
//
// DEPRECATED with the alias itself: delete this test when /api/v1/xyp goes.
func TestThePreMoveLookupAddressStillAnswers(t *testing.T) {
	f := newFixture(t)

	if res := f.do(t, http.MethodPost, "/api/v1/xyp/citizen", `{"reg_number":"AA90010111"}`); res.Code != http.StatusOK {
		t.Fatalf("the old citizen address answered %d: %s", res.Code, res.Body.String())
	}
	if res := f.do(t, http.MethodPost, "/api/v1/xyp/company", `{"company_reg":"5551234"}`); res.Code != http.StatusOK {
		t.Fatalf("the old company address answered %d: %s", res.Code, res.Body.String())
	}
}

func TestConnectionsReportTheRailModeAndPointAtTheProfileForIdentities(t *testing.T) {
	f := newFixture(t)

	res := f.do(t, http.MethodGet, "/api/v1/egov/connections", "")
	if res.Code != http.StatusOK {
		t.Fatalf("connections answered %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Rails          []nexus.StateRail `json:"rails"`
		IdentitiesPath string            `json:"identities_path"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Rails) != 1 || body.Rails[0].Mode != "mock" {
		t.Fatalf("a mock rail must say so: %+v", body.Rails)
	}
	// The screen points at the person's own identities rather than owning
	// them: a build that moved them in here would drop this field, and an
	// administrator would gain the ability to unlink somebody else's national
	// identity by uninstalling an app.
	if body.IdentitiesPath != "/profile" {
		t.Fatalf("connections should send people to their own profile, got %q", body.IdentitiesPath)
	}
}
