package organisation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/gerege-systems/client-gerege-nexus/modules/organisation"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The organisation and its people are mostly SQL: a profile row that must exist
// for every tenant, composite foreign keys that refuse a department from
// another organisation, and the two refusals that keep somebody from locking
// the last administrator out. None of that is observable without a database.
//
//	TEST_DATABASE_URL=postgres://... go test ./internal/apps/organisation/...
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a migrated database to run the organisation tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type fixture struct {
	membershipID string
	people       *fakeDirectory
	pool         *pgxpool.Pool
	router       chi.Router
	tenantID     string
	userID       string
	otherID      string // a second tenant, to prove nothing leaks between them
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()

	newTenant := func(prefix string) string {
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO tenants (slug, name) VALUES ($1 || substr(gen_random_uuid()::text, 1, 8), 'Core test')
			 RETURNING id::text`, prefix).Scan(&id); err != nil {
			t.Fatalf("tenant: %v", err)
		}
		// No profile is inserted here on purpose. It arrives with the tenant, by
		// trigger, and every read below would fail if it did not — which is the
		// only way that invariant is worth relying on.
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })
		return id
	}

	f := &fixture{pool: pool, tenantID: newTenant("org-"), otherID: newTenant("other-")}

	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, is_admin)
		 VALUES ('org-' || substr(gen_random_uuid()::text, 1, 8) || '@example.com', 'x', 'Org Admin', TRUE)
		 RETURNING id::text`).Scan(&f.userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.userID) })

	// The membership is the platform's row and reaches this module through
	// nexus.Directory, so the fake below is where it lives. It is still written
	// to the database: organisation_people references memberships(id), which is
	// this app's foreign key into the platform's table and the one thing about
	// the split that is not a contract.
	if err := pool.QueryRow(ctx,
		`INSERT INTO memberships (tenant_id, user_id) VALUES ($1, $2) RETURNING id::text`,
		f.tenantID, f.userID).Scan(&f.membershipID); err != nil {
		t.Fatalf("membership: %v", err)
	}

	// The module's own router, with the tenant and the caller already resolved:
	// what is under test is the module, not the middleware in front of it.
	// The directory is provided, not queried. This module used to read the
	// platform's memberships itself; it asks now, and a test stands the
	// implementation up the same way a deployment does.
	people := &fakeDirectory{}
	people.add(nexus.DirectoryPerson{
		MembershipID: f.membershipID, UserID: f.userID, Name: "Org Admin",
		Email: "org-admin@example.invalid", Active: true, IsAdmin: true,
		Roles: []string{"admin"}, TenantID: f.tenantID, TenantName: "Org",
	})
	f.people = people
	nexus.Provide[nexus.Directory](people)
	t.Cleanup(func() { nexus.Provide[nexus.Directory](nil) })
	module := organisation.New(nexus.NewPlatform(pool, everyPermission{}))
	router := chi.NewRouter()
	module.RegisterRoutes(router, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := nexus.WithTenantID(r.Context(), f.tenantID)
			ctx = nexus.WithUser(ctx, nexus.UserClaims{UserID: f.userID, TenantID: f.tenantID, IsAdmin: true})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	f.router = router
	return f
}

func (f *fixture) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestADepartmentFromAnotherOrganisationIsRefused(t *testing.T) {
	f := newFixture(t)

	// A department in the *other* tenant, offered here as a parent.
	var foreignID string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO departments (tenant_id, code, name) VALUES ($1, 'foreign', 'Foreign')
		 RETURNING id::text`, f.otherID).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}

	res := f.do(t, http.MethodPost, "/api/v1/organisation/departments",
		`{"code":"sales","name":"Борлуулалт","parent_id":"`+foreignID+`"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected a parent from another organisation to be refused, got %d: %s",
			res.Code, res.Body.String())
	}

	// And the same department without the foreign parent is fine, so the
	// refusal is about the tenant rather than about the request being malformed.
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments",
		`{"code":"sales","name":"Борлуулалт"}`); res.Code != http.StatusCreated {
		t.Fatalf("expected the department to be created, got %d: %s", res.Code, res.Body.String())
	}
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments",
		`{"code":"sales","name":"Дахин"}`); res.Code != http.StatusConflict {
		t.Fatalf("expected a duplicate code to be refused, got %d", res.Code)
	}
}

func TestYouCannotDeactivateYourselfOrTheLastAdministrator(t *testing.T) {
	f := newFixture(t)

	var membershipID string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM memberships WHERE tenant_id = $1 AND user_id = $2`,
		f.tenantID, f.userID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}

	// Both refusals apply to this one membership — it is the caller's, and its
	// user is the only administrator — and either is enough to stop it.
	res := f.do(t, http.MethodPost, "/api/v1/organisation/people/"+membershipID+"/deactivate", "")
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected self-deactivation to be refused, got %d: %s", res.Code, res.Body.String())
	}

	var active bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT active FROM memberships WHERE id = $1`, membershipID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("the membership was deactivated despite the refusal")
	}
}

func TestPeopleAreListedWithWhatThisOrganisationKnows(t *testing.T) {
	f := newFixture(t)

	created := f.do(t, http.MethodPost, "/api/v1/organisation/departments", `{"code":"it","name":"Мэдээллийн технологи"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("department: %d", created.Code)
	}
	var department struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &department); err != nil {
		t.Fatal(err)
	}

	var membershipID string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM memberships WHERE tenant_id = $1`, f.tenantID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/organisation/people/"+membershipID,
		`{"job_title":"Захирал","department_id":"`+department.ID+`"}`); res.Code != http.StatusOK {
		t.Fatalf("update person: %d %s", res.Code, res.Body.String())
	}

	var people []organisation.Person
	if err := json.Unmarshal(f.do(t, http.MethodGet, "/api/v1/organisation/people", "").Body.Bytes(), &people); err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 {
		t.Fatalf("expected one person, got %d", len(people))
	}
	if people[0].JobTitle != "Захирал" || people[0].DepartmentName != "Мэдээллийн технологи" {
		t.Fatalf("the membership's own fields did not come back: %+v", people[0])
	}
	// The identity comes from the user and is shared across organisations; the
	// job title is the membership's. Both have to arrive in one row for the
	// screen to be a directory rather than two lists.
	if people[0].Email == "" || people[0].Name == "" {
		t.Fatalf("the person's identity is missing: %+v", people[0])
	}
}

// Archiving exists so that a unit with people and history behind it can leave
// the lists without taking them with it. That is only true if it can come back.
func TestAnArchivedDepartmentCanComeBack(t *testing.T) {
	f := newFixture(t)

	create := func(code, name, parent string) string {
		t.Helper()
		body := `{"code":"` + code + `","name":"` + name + `"`
		if parent != "" {
			body += `,"parent_id":"` + parent + `"`
		}
		res := f.do(t, http.MethodPost, "/api/v1/organisation/departments", body+`}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", code, res.Code, res.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}

	parent := create("ops", "Үйл ажиллагаа", "")
	child := create("ops-sales", "Борлуулалт", parent)

	// Archived, then brought back on its own: the ordinary case.
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+child+"/archive", ""); res.Code != http.StatusOK {
		t.Fatalf("archive: %d", res.Code)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+child+"/restore", ""); res.Code != http.StatusOK {
		t.Fatalf("restore answered %d: %s", res.Code, res.Body.String())
	}
	var active bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT active FROM departments WHERE id = $1`, child).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("the department did not come back")
	}

	// And the one case that is refused: a unit cannot stand under a parent that
	// is still archived, or the tree draws it as a root with a parent missing
	// from every list that offers one.
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+parent+"/archive", ""); res.Code != http.StatusOK {
		t.Fatalf("archive parent: %d", res.Code)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+child+"/archive", ""); res.Code != http.StatusOK {
		t.Fatalf("archive child: %d", res.Code)
	}
	res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+child+"/restore", "")
	if res.Code != http.StatusConflict {
		t.Fatalf("expected a refusal while the parent is archived, got %d: %s", res.Code, res.Body.String())
	}
	// The refusal names the unit that has to come back first, because that is
	// the whole of what the operator has to do next.
	if !strings.Contains(res.Body.String(), "Үйл ажиллагаа") {
		t.Fatalf("the refusal should name the parent; got %s", res.Body.String())
	}

	// Parent first, then the child: the order the refusal asked for.
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+parent+"/restore", ""); res.Code != http.StatusOK {
		t.Fatalf("restore parent: %d", res.Code)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/organisation/departments/"+child+"/restore", ""); res.Code != http.StatusOK {
		t.Fatalf("restore child after its parent: %d %s", res.Code, res.Body.String())
	}
}

// Deleting and archiving are different acts, and the screen now offers both.
// The delete is for a unit that never really existed — a typo, a duplicate —
// and it has to stay refused the moment anything points at the row, or it
// becomes a way to lose people quietly.
func TestAUnitIsDeletedOnlyWhenNothingPointsAtIt(t *testing.T) {
	f := newFixture(t)

	create := func(code, name, parent string) string {
		t.Helper()
		body := `{"code":"` + code + `","name":"` + name + `"`
		if parent != "" {
			body += `,"parent_id":"` + parent + `"`
		}
		res := f.do(t, http.MethodPost, "/api/v1/organisation/departments", body+`}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", code, res.Code, res.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}

	parent := create("ops", "Үйл ажиллагаа", "")
	child := create("ops-sales", "Борлуулалт", parent)

	// A unit with something under it is refused, and told what is under it.
	res := f.do(t, http.MethodDelete, "/api/v1/organisation/departments/"+parent, "")
	if res.Code != http.StatusConflict {
		t.Fatalf("expected a unit with children to be refused, got %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "1 units") {
		t.Fatalf("the refusal should count what is in the way; got %s", res.Body.String())
	}

	// Somebody working in it counts too.
	var membershipID string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM memberships WHERE tenant_id = $1`, f.tenantID).Scan(&membershipID); err != nil {
		t.Fatal(err)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/organisation/people/"+membershipID,
		`{"department_id":"`+child+`"}`); res.Code != http.StatusOK {
		t.Fatalf("assign: %d", res.Code)
	}
	if res := f.do(t, http.MethodDelete, "/api/v1/organisation/departments/"+child, ""); res.Code != http.StatusConflict {
		t.Fatalf("expected a unit with people to be refused, got %d: %s", res.Code, res.Body.String())
	}

	// Emptied, it goes.
	if res := f.do(t, http.MethodPut, "/api/v1/organisation/people/"+membershipID, `{"department_id":""}`); res.Code != http.StatusOK {
		t.Fatalf("unassign: %d", res.Code)
	}
	if res := f.do(t, http.MethodDelete, "/api/v1/organisation/departments/"+child, ""); res.Code != http.StatusOK {
		t.Fatalf("delete answered %d: %s", res.Code, res.Body.String())
	}
	var left int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM departments WHERE id = $1`, child).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the department is still there")
	}
}

// The tree has to stay a tree. A unit moved under one of its own descendants
// would make every reader that follows parent_id loop for ever, and no CHECK
// can see it — it is a walk, not a row.
func TestAUnitCannotBeMovedUnderItsOwnDescendant(t *testing.T) {
	f := newFixture(t)

	create := func(code, name, parent string) string {
		t.Helper()
		body := `{"code":"` + code + `","name":"` + name + `"`
		if parent != "" {
			body += `,"parent_id":"` + parent + `"`
		}
		res := f.do(t, http.MethodPost, "/api/v1/organisation/departments", body+`}`)
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", code, res.Code)
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}

	top := create("top", "Тэргүүн", "")
	middle := create("middle", "Дунд", top)
	bottom := create("bottom", "Доод", middle)

	// Two levels down, so a check that only looked at the immediate children
	// would let this through.
	res := f.do(t, http.MethodPut, "/api/v1/organisation/departments/"+top,
		`{"name":"Тэргүүн","parent_id":"`+bottom+`"}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("expected the loop to be refused, got %d: %s", res.Code, res.Body.String())
	}

	// And itself, which the schema also refuses but which deserves words.
	if res := f.do(t, http.MethodPut, "/api/v1/organisation/departments/"+middle,
		`{"name":"Дунд","parent_id":"`+middle+`"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("expected self-parenting to be refused, got %d", res.Code)
	}
}
