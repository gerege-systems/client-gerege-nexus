package documents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Which permission guards which route used to be decided by the platform, by
// matching on the URL text, and a table in access_control_test.go asserted the
// whole mapping. Moving the decision onto the routes themselves was the right
// change and it took that table with it: a hand slipping from dr.With(sign) to
// dr.With(manage) is now a silent downgrade — an approver's authority handed to
// anybody who may draft.
//
// This is that table, restated against the router that actually serves.

// grants answers as a member holding exactly one permission.
type grants map[string]bool

func (g grants) GetUserPermissions(context.Context, string, string) (map[string]bool, error) {
	return g, nil
}

// routerFor builds the module's real route table with a stubbed permission
// store. The pool points at a port nothing listens on: a handler that is
// reached fails on the database instead of panicking, which is all this test
// needs — it asks whether the request got past the guard, not what it did next.
func routerFor(t *testing.T, held string) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	module := &DocumentsModule{db: pool, perms: grants{held: true}}
	router := chi.NewRouter()
	// The tenant gate is the platform's; here it only has to supply the context
	// the permission middleware reads.
	module.RegisterRoutes(router, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := nexus.WithUser(r.Context(), nexus.UserClaims{
				UserID: "11111111-1111-1111-1111-111111111111",
				// Not an administrator: RequirePermission waves those through,
				// which would make every case below pass for the wrong reason.
				TenantID: "22222222-2222-2222-2222-222222222222",
				Email:    "member@example.mn",
			})
			ctx = nexus.WithTenantID(ctx, "22222222-2222-2222-2222-222222222222")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	return router
}

func TestEveryDocumentsRouteIsGuardedByThePermissionItClaims(t *testing.T) {
	const id = "3f1b9c62-2f1a-4a1c-9d3e-8b7a5c4e1d20"

	cases := []struct {
		method, path, want string
	}{
		// Reading.
		{http.MethodGet, "/api/v1/documents/", "documents.read"},
		{http.MethodGet, "/api/v1/documents/templates", "documents.read"},
		{http.MethodGet, "/api/v1/documents/policies", "documents.read"},
		{http.MethodGet, "/api/v1/documents/workflows", "documents.read"},
		{http.MethodGet, "/api/v1/documents/retention", "documents.read"},
		{http.MethodGet, "/api/v1/documents/" + id + "/signatures", "documents.read"},
		{http.MethodGet, "/api/v1/documents/" + id + "/steps", "documents.read"},

		// Authoring and configuring. Routing a document for approval is
		// authoring: a clerk may send on what they may not approve.
		{http.MethodPost, "/api/v1/documents/", "documents.manage"},
		{http.MethodPost, "/api/v1/documents/templates", "documents.manage"},
		{http.MethodPut, "/api/v1/documents/templates/" + id, "documents.manage"},
		{http.MethodDelete, "/api/v1/documents/templates/" + id, "documents.manage"},
		{http.MethodPost, "/api/v1/documents/templates/" + id + "/use", "documents.manage"},
		{http.MethodPut, "/api/v1/documents/policies/CONTRACT", "documents.manage"},
		{http.MethodPut, "/api/v1/documents/workflows/CONTRACT", "documents.manage"},
		{http.MethodPut, "/api/v1/documents/retention/CONTRACT", "documents.manage"},
		{http.MethodPut, "/api/v1/documents/" + id + "/title", "documents.manage"},
		{http.MethodPost, "/api/v1/documents/" + id + "/route", "documents.manage"},

		// Deciding. Signing and refusing to sign are the same authority, and it
		// is not the authority to draft.
		{http.MethodPost, "/api/v1/documents/" + id + "/reject", "documents.sign"},
		{http.MethodPost, "/api/v1/documents/" + id + "/sign/dan", "documents.sign"},
		{http.MethodPost, "/api/v1/documents/" + id + "/sign/eid/start", "documents.sign"},
		{http.MethodPost, "/api/v1/documents/" + id + "/sign/eid/poll", "documents.sign"},
	}

	all := []string{"documents.read", "documents.manage", "documents.sign"}

	for _, tc := range cases {
		name := tc.method + " " + strings.Replace(tc.path, id, "{id}", 1)
		t.Run(name, func(t *testing.T) {
			for _, held := range all {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				routerFor(t, held).ServeHTTP(rec, req)

				forbidden := rec.Code == http.StatusForbidden
				switch {
				case held == tc.want && forbidden:
					t.Errorf("holding %s was refused; this route claims %s", held, tc.want)
				case held != tc.want && !forbidden:
					t.Errorf("holding only %s got past a route that should need %s (status %d)",
						held, tc.want, rec.Code)
				}
			}
		})
	}
}

// A route added later with no permission at all would pass the table above by
// simply not being in it. This is the check that notices.
func TestNoDocumentsRouteIsUnguarded(t *testing.T) {
	router := routerFor(t, "documents.read").(*chi.Mux)

	var unguarded []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet || method == http.MethodHead {
			return nil // covered by documents.read, which the stub holds
		}
		// Every write must be refused for a member holding only the read right.
		path := strings.ReplaceAll(route, "/*", "")
		path = strings.NewReplacer("{id}", "3f1b9c62-2f1a-4a1c-9d3e-8b7a5c4e1d20",
			"{docType}", "CONTRACT").Replace(path)

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		routerFor(t, "documents.read").ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			unguarded = append(unguarded, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(unguarded) > 0 {
		t.Errorf("these writes were reachable holding only documents.read: %v", unguarded)
	}
}
