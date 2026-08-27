package reports_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gerege-systems/client-gerege-nexus/modules/reports"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// What this app answers today, written down before it is taken apart.
//
// The reports app had no tests at all. Its rules — which reports a tenant may
// see, what a schedule body has to satisfy, who may accept a sharing request —
// live in HTTP handlers and were only ever checked by hand. That is a poor
// position from which to move code: a refactor with no net cannot say whether
// it changed anything, and the things most easily broken here (the app gate on
// a report key, the grantor-only accept) are the ones nobody notices until the
// wrong organisation has read something.
//
// So these run first and unchanged through everything that follows. They assert
// status codes and stored rows rather than internals, which is what makes them
// still true on the other side of the move.
//
//	REPORTING_TEST_DATABASE_URL=postgres://... go test ./internal/apps/reports/...

// installedApp is the app the test tenant has; absentApp is one it does not.
const (
	installedApp = "io.gerege.nexus.reports"
	absentApp    = "io.gerege.test.absent"
)

// The two reports these tests are about, as catalogue entries.
//
// They were Go types implementing internal/platform/reporting.Report, declared
// here and registered into the engine's own registry. The app never sees a
// report that way any more — it asks the engine for descriptions — so what a
// test has to supply is descriptions.
const (
	sharedKey = "test.shared"
	absentKey = "test.absent"
)

func catalogue() fakeEngine {
	return fakeEngine{reports: []nexus.ReportDescription{
		{
			Key: sharedKey, App: installedApp,
			Titles: map[string]string{"mn": "Туршилтын тайлан", "en": "Test report"},
			// Shareable both ways, so the scope rules have something to accept
			// as well as something to refuse.
			Scopes: []string{nexus.ReportScopeCounterparty, nexus.ReportScopeFull},
		},
		{
			// Belongs to an app the tenant has not installed, and declares no
			// scopes, so it is also the report that cannot be shared at all.
			Key: absentKey, App: absentApp,
			Titles: map[string]string{"mn": "Байхгүй апп", "en": "Absent app"},
		},
	}}
}

type fixture struct {
	tenantID  string // the organisation making the requests
	otherID   string // a second one, to ask for a report from and to refuse
	userID    string
	otherReg  string
	ownReg    string
	schedules *fakeSchedules
	grants    *fakeGrants
	module    *reports.Module
}

// newFixture builds the app over the three contracts and nothing else.
//
// There is no database here and no skip: the app holds no handle since the
// contracts landed, so what these tests need is what it actually depends on.
// The rows they used to select are the fakes' maps.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		tenantID:  uuid.NewString(),
		otherID:   uuid.NewString(),
		userID:    uuid.NewString(),
		ownReg:    "1234567",
		otherReg:  "7654321",
		schedules: newFakeSchedules(),
		grants:    newFakeGrants(),
	}
	f.grants.organisation(f.tenantID, f.ownReg)
	f.grants.organisation(f.otherID, f.otherReg)

	// The app gate, handed in as the platform hands it in: this deployment has
	// the reports app and not the absent one.
	installed := func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{installedApp: true}, nil
	}
	f.module = reports.New(nexus.NewPlatform(nil, everyPermission{}), installed,
		catalogue(), f.schedules, f.grants)
	return f
}

// as mounts the module for one organisation. Two are needed: a sharing request
// is made by one and accepted by the other, and which side is asking is the
// whole of what the accept endpoint checks.
func (f *fixture) as(tenantID string) chi.Router {
	router := chi.NewRouter()
	f.module.RegisterRoutes(router, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := nexus.WithWorkspaceID(r.Context(), tenantID)
			ctx = nexus.WithUser(ctx, nexus.UserClaims{UserID: f.userID, WorkspaceID: tenantID, IsAdmin: true})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	return router
}

func do(t *testing.T, router chi.Router, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// The app gate is the one check that must not be skipped: without it a caller
// who knows a key runs a report belonging to an app their organisation never
// installed.
func TestOnlyTheReportsOfInstalledAppsAreReachable(t *testing.T) {
	f := newFixture(t)
	router := f.as(f.tenantID)

	list := do(t, router, http.MethodGet, "/api/v1/reports/", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if !strings.Contains(body, sharedKey) {
		t.Fatalf("the installed app's report is missing from the list: %s", body)
	}
	if strings.Contains(body, absentKey) {
		t.Fatalf("a report from an app this organisation does not have was listed: %s", body)
	}

	// Named directly, the same answer, and 404 rather than 403 — the two
	// together would enumerate the catalogue.
	for _, target := range []string{
		"/api/v1/reports/" + absentKey,
		"/api/v1/reports/" + absentKey + "/run",
		"/api/v1/reports/nope.at.all",
	} {
		method := http.MethodGet
		if strings.HasSuffix(target, "/run") {
			method = http.MethodPost
		}
		if res := do(t, router, method, target, ""); res.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404: %s", target, res.Code, res.Body.String())
		}
	}

	if res := do(t, router, http.MethodGet, "/api/v1/reports/"+sharedKey, ""); res.Code != http.StatusOK {
		t.Fatalf("metadata: %d %s", res.Code, res.Body.String())
	}
	if res := do(t, router, http.MethodPost, "/api/v1/reports/"+sharedKey+"/run", ""); res.Code != http.StatusOK {
		t.Fatalf("run: %d %s", res.Code, res.Body.String())
	}
	export := do(t, router, http.MethodPost, "/api/v1/reports/"+sharedKey+"/export?format=csv", "")
	if export.Code != http.StatusOK {
		t.Fatalf("export: %d %s", export.Code, export.Body.String())
	}
	if disposition := export.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") {
		t.Fatalf("an export should arrive as a file; got %q", disposition)
	}
	if export.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("an export is a tenant's data and must not be cached")
	}
}

// A schedule is stored and run weeks later, so everything wrong with it has to
// be said now, while somebody is still present to be told.
func TestAScheduleIsCheckedBeforeItIsStored(t *testing.T) {
	f := newFixture(t)
	router := f.as(f.tenantID)

	good := `{"report_key":"` + sharedKey + `","name":"Сар бүр","cron":"0 6 1 * *",` +
		`"format":"xlsx","recipients":["A@Example.com","a@example.com","b@example.com"]}`

	for name, body := range map[string]string{
		"unknown report":   `{"report_key":"nope","cron":"0 6 1 * *","recipients":["a@example.com"]}`,
		"uninstalled app":  `{"report_key":"` + absentKey + `","cron":"0 6 1 * *","recipients":["a@example.com"]}`,
		"unparseable cron": `{"report_key":"` + sharedKey + `","cron":"every monday","recipients":["a@example.com"]}`,
		"unknown format":   `{"report_key":"` + sharedKey + `","cron":"0 6 1 * *","format":"pdf","recipients":["a@example.com"]}`,
		"no recipients":    `{"report_key":"` + sharedKey + `","cron":"0 6 1 * *","recipients":[]}`,
		"not an address":   `{"report_key":"` + sharedKey + `","cron":"0 6 1 * *","recipients":["not an address"]}`,
		"malformed body":   `{`,
	} {
		if res := do(t, router, http.MethodPost, "/api/v1/reports/schedules", body); res.Code != http.StatusBadRequest {
			t.Fatalf("%s answered %d, want 400: %s", name, res.Code, res.Body.String())
		}
	}

	created := do(t, router, http.MethodPost, "/api/v1/reports/schedules", good)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	// Addresses are lowercased and de-duplicated, and a schedule nobody said
	// anything about is active.
	stored, found := f.schedules.stored(body.ID)
	if !found {
		t.Fatal("the schedule was accepted and not stored")
	}
	if len(stored.Recipients) != 2 || stored.Recipients[0] != "a@example.com" || stored.Recipients[1] != "b@example.com" {
		t.Fatalf("the address list was not cleaned up: %v", stored.Recipients)
	}
	if !stored.Active {
		t.Fatal("a schedule with no active flag should be active")
	}

	if res := do(t, router, http.MethodGet, "/api/v1/reports/schedules", ""); res.Code != http.StatusOK {
		t.Fatalf("list schedules: %d", res.Code)
	}
	if res := do(t, router, http.MethodPut, "/api/v1/reports/schedules/not-a-uuid", good); res.Code != http.StatusBadRequest {
		t.Fatalf("an unparseable id answered %d, want 400", res.Code)
	}
	if res := do(t, router, http.MethodPut, "/api/v1/reports/schedules/"+uuid.NewString(), good); res.Code != http.StatusNotFound {
		t.Fatalf("an unknown schedule answered %d, want 404", res.Code)
	}
	if res := do(t, router, http.MethodPut, "/api/v1/reports/schedules/"+body.ID, good); res.Code != http.StatusOK {
		t.Fatalf("update: %d %s", res.Code, res.Body.String())
	}
	if res := do(t, router, http.MethodDelete, "/api/v1/reports/schedules/"+body.ID, ""); res.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", res.Code, res.Body.String())
	}
	if res := do(t, router, http.MethodDelete, "/api/v1/reports/schedules/"+body.ID, ""); res.Code != http.StatusNotFound {
		t.Fatalf("deleting twice answered %d, want 404", res.Code)
	}
}

// Sharing is the one thing here that crosses an organisation's boundary, so
// every refusal in it is load-bearing: who may ask, who may agree, and what
// happens to a report that was never written to be shared.
func TestOnlyTheOwnerAgreesToShareAndEitherSideMayEnd(t *testing.T) {
	f := newFixture(t)
	grantee := f.as(f.tenantID)
	grantor := f.as(f.otherID)

	ask := func(fields string) *httptest.ResponseRecorder {
		return do(t, grantee, http.MethodPost, "/api/v1/reports/grants", "{"+fields+"}")
	}

	if res := ask(`"grantor_registration_number":"NOT-A-REG","report_key":"` + sharedKey + `"`); res.Code != http.StatusNotFound {
		t.Fatalf("an unknown registration number answered %d, want 404: %s", res.Code, res.Body.String())
	}
	for name, fields := range map[string]string{
		"itself":             `"grantor_registration_number":"` + f.ownReg + `","report_key":"` + sharedKey + `"`,
		"unknown report":     `"grantor_registration_number":"` + f.otherReg + `","report_key":"nope"`,
		"invented scope":     `"grantor_registration_number":"` + f.otherReg + `","report_key":"` + sharedKey + `","scope":"everything"`,
		"unshareable report": `"grantor_registration_number":"` + f.otherReg + `","report_key":"` + absentKey + `"`,
		"bad date":           `"grantor_registration_number":"` + f.otherReg + `","report_key":"` + sharedKey + `","valid_until":"01.01.2027"`,
	} {
		if res := ask(fields); res.Code != http.StatusBadRequest {
			t.Fatalf("%s answered %d, want 400: %s", name, res.Code, res.Body.String())
		}
	}

	created := ask(`"grantor_registration_number":"` + f.otherReg + `","report_key":"` + sharedKey + `","scope":"full"`)
	if created.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", created.Code, created.Body.String())
	}
	var grant struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}

	// One live agreement per pair per report, held by a partial unique index.
	if res := ask(`"grantor_registration_number":"` + f.otherReg + `","report_key":"` + sharedKey + `","scope":"full"`); res.Code != http.StatusConflict {
		t.Fatalf("a second request answered %d, want 409", res.Code)
	}

	// The asking side cannot agree on the owner's behalf. This is the whole of
	// what the accept endpoint is for.
	if res := do(t, grantee, http.MethodPost, "/api/v1/reports/grants/"+grant.ID+"/accept", ""); res.Code != http.StatusNotFound {
		t.Fatalf("the grantee accepting its own request answered %d, want 404: %s", res.Code, res.Body.String())
	}
	if res := do(t, grantor, http.MethodPost, "/api/v1/reports/grants/"+grant.ID+"/accept", ""); res.Code != http.StatusOK {
		t.Fatalf("the owner accepting answered %d: %s", res.Code, res.Body.String())
	}
	if res := do(t, grantor, http.MethodPost, "/api/v1/reports/grants/"+grant.ID+"/accept", ""); res.Code != http.StatusNotFound {
		t.Fatalf("accepting twice answered %d, want 404", res.Code)
	}

	if res := do(t, grantee, http.MethodGet, "/api/v1/reports/grants", ""); res.Code != http.StatusOK {
		t.Fatalf("list grants: %d", res.Code)
	}
	if res := do(t, grantor, http.MethodGet, "/api/v1/reports/grants/history", ""); res.Code != http.StatusOK {
		t.Fatalf("access history: %d", res.Code)
	}

	// Either side may end it, and the row stays: "who could see our data, and
	// when" outlives the agreement.
	if res := do(t, grantee, http.MethodPost, "/api/v1/reports/grants/"+grant.ID+"/revoke", ""); res.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", res.Code, res.Body.String())
	}
	if res := do(t, grantee, http.MethodPost, "/api/v1/reports/grants/"+grant.ID+"/revoke", ""); res.Code != http.StatusNotFound {
		t.Fatalf("revoking twice answered %d, want 404", res.Code)
	}
	if res := do(t, grantee, http.MethodPost, "/api/v1/reports/grants/not-a-uuid/revoke", ""); res.Code != http.StatusBadRequest {
		t.Fatalf("an unparseable grant id answered %d, want 400", res.Code)
	}
	ended, found := f.grants.stored(grant.ID)
	if !found || ended.RevokedAt == nil {
		t.Fatal("a revoked agreement should still be there to read")
	}
}
