package organisation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gerege-systems/client-gerege-nexus/domain/organisation"
	"github.com/gerege-systems/client-gerege-nexus/domain/organisation/memory"
)

// The five rules this app is, run without a database.
//
// Every one of them used to need a migrated PostgreSQL and an HTTP request to
// observe, which is why they were tested in one file that skips itself when
// TEST_DATABASE_URL is unset — green on a laptop having asserted nothing. They
// are decisions Go makes; this is where they are checked. The SQL is still
// covered by internal/apps/organisation/module_test.go, which is about the
// schema and the routes.

const tenant = "tenant-1"

func newService(t *testing.T) (*organisation.Service, *memory.Store) {
	t.Helper()
	store := memory.New()
	return organisation.NewService(store), store
}

func create(t *testing.T, s *organisation.Service, code, name, parent string) string {
	t.Helper()
	edit := organisation.DepartmentEdit{Code: code, Name: name}
	if parent != "" {
		edit.ParentID = &parent
	}
	id, err := s.CreateDepartment(context.Background(), tenant, edit)
	if err != nil {
		t.Fatalf("create %s: %v", code, err)
	}
	return id
}

// Locking yourself out is a support ticket, and an organisation with nobody who
// can administer it is a worse one: both refusals apply to the same membership
// when the only administrator is the caller.
func TestNobodyDeactivatesThemselvesOrTheLastAdministrator(t *testing.T) {
	service, store := newService(t)
	admin := store.AddPerson(organisation.Person{TenantID: tenant, Active: true, IsAdmin: true})
	clerk := store.AddPerson(organisation.Person{TenantID: tenant, Active: true})

	// Their own membership, by the person holding it.
	err := service.SetPersonActive(context.Background(), tenant, admin.MembershipID, admin.UserID, false)
	if !errors.Is(err, organisation.ErrSelfDeactivation) {
		t.Fatalf("expected self-deactivation to be refused, got %v", err)
	}

	// Somebody else's, when they are the last administrator left.
	err = service.SetPersonActive(context.Background(), tenant, admin.MembershipID, clerk.UserID, false)
	if !errors.Is(err, organisation.ErrLastAdministrator) {
		t.Fatalf("expected the last administrator to be kept, got %v", err)
	}

	// A second administrator, and the first may go.
	store.AddPerson(organisation.Person{TenantID: tenant, Active: true, IsAdmin: true})
	if err := service.SetPersonActive(context.Background(), tenant, admin.MembershipID, clerk.UserID, false); err != nil {
		t.Fatalf("expected the deactivation to be allowed once another admin exists: %v", err)
	}

	// And coming back is never refused: neither rule is about arriving.
	if err := service.SetPersonActive(context.Background(), tenant, admin.MembershipID, admin.UserID, true); err != nil {
		t.Fatalf("expected a reactivation to be allowed: %v", err)
	}
}

// A unit moved under one of its own descendants would make every reader that
// follows parent_id loop for ever, and no CHECK can see it — it is a walk, not
// a row.
func TestAUnitCannotBeMovedUnderItsOwnDescendant(t *testing.T) {
	service, _ := newService(t)
	top := create(t, service, "top", "Тэргүүн", "")
	middle := create(t, service, "middle", "Дунд", top)
	bottom := create(t, service, "bottom", "Доод", middle)

	// Two levels down, so a check that only looked at the immediate children
	// would let this through.
	err := service.UpdateDepartment(context.Background(), tenant, top,
		organisation.DepartmentEdit{Name: "Тэргүүн", ParentID: &bottom})
	if !errors.Is(err, organisation.ErrCycle) {
		t.Fatalf("expected the loop to be refused, got %v", err)
	}

	// And itself, which the schema also refuses but which deserves words.
	if err := service.UpdateDepartment(context.Background(), tenant, middle,
		organisation.DepartmentEdit{Name: "Дунд", ParentID: &middle}); !errors.Is(err, organisation.ErrSelfParent) {
		t.Fatalf("expected self-parenting to be refused, got %v", err)
	}

	// Sideways is fine — the rule is about descendants, not about moving.
	if err := service.UpdateDepartment(context.Background(), tenant, bottom,
		organisation.DepartmentEdit{Name: "Доод", ParentID: &top}); err != nil {
		t.Fatalf("expected an ordinary move to be allowed: %v", err)
	}
}

// A unit cannot come back under a parent that is still archived: the tree would
// draw it as a root, its own parent would be missing from every list that
// offers one, and the next edit would silently reparent it.
func TestAnArchivedParentHoldsItsChildBack(t *testing.T) {
	service, _ := newService(t)
	parent := create(t, service, "ops", "Үйл ажиллагаа", "")
	child := create(t, service, "ops-sales", "Борлуулалт", parent)

	for _, id := range []string{child, parent} {
		if err := service.SetDepartmentArchived(context.Background(), tenant, id, true); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}

	err := service.SetDepartmentArchived(context.Background(), tenant, child, false)
	if !errors.Is(err, organisation.ErrParentArchived) {
		t.Fatalf("expected the restore to be refused, got %v", err)
	}
	// The refusal names the unit that has to come back first, because that is
	// the whole of what the operator has to do next.
	if err.Error() != "the unit this one reports to is archived; restore Үйл ажиллагаа first" {
		t.Fatalf("the refusal should name the parent; got %q", err)
	}

	// Parent first, then the child: the order the refusal asked for.
	if err := service.SetDepartmentArchived(context.Background(), tenant, parent, false); err != nil {
		t.Fatalf("restore parent: %v", err)
	}
	if err := service.SetDepartmentArchived(context.Background(), tenant, child, false); err != nil {
		t.Fatalf("restore child after its parent: %v", err)
	}
}

// Deleting is for a unit that never really existed. It stays refused the moment
// anything points at the row, or it becomes a way to lose people quietly.
func TestAUnitIsDeletedOnlyWhenNothingPointsAtIt(t *testing.T) {
	service, store := newService(t)
	parent := create(t, service, "ops", "Үйл ажиллагаа", "")
	child := create(t, service, "ops-sales", "Борлуулалт", parent)
	person := store.AddPerson(organisation.Person{TenantID: tenant, Active: true, DepartmentID: child})

	err := service.DeleteDepartment(context.Background(), tenant, parent)
	if !errors.Is(err, organisation.ErrUnitNotEmpty) {
		t.Fatalf("expected a unit with children to be refused, got %v", err)
	}
	// The refusal counts what is in the way rather than only saying no.
	if err.Error() != "this unit still has 0 people and 1 units under it; move them first, or archive it instead" {
		t.Fatalf("the refusal should count what is in the way; got %q", err)
	}

	if err := service.DeleteDepartment(context.Background(), tenant, child); !errors.Is(err, organisation.ErrUnitNotEmpty) {
		t.Fatalf("expected a unit with people to be refused, got %v", err)
	}

	// Emptied, it goes.
	nowhere := ""
	if err := service.UpdatePerson(context.Background(), tenant, person.MembershipID,
		organisation.PersonEdit{DepartmentID: &nowhere}); err != nil {
		t.Fatalf("unassign: %v", err)
	}
	if err := service.DeleteDepartment(context.Background(), tenant, child); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := service.DeleteDepartment(context.Background(), tenant, child); !errors.Is(err, organisation.ErrNotFound) {
		t.Fatalf("expected the second delete to find nothing, got %v", err)
	}
}

// A code is a store URL segment and a manifest filename elsewhere in the
// platform, and this app holds departments to the same rule.
func TestADepartmentNeedsANameAndACatalogueCode(t *testing.T) {
	service, _ := newService(t)

	for _, code := range []string{"", "Sales", "sales dept", "борлуулалт", "sales/../etc"} {
		if _, err := service.CreateDepartment(context.Background(), tenant,
			organisation.DepartmentEdit{Code: code, Name: "Борлуулалт"}); !errors.Is(err, organisation.ErrInvalidCode) {
			t.Fatalf("expected %q to be refused, got %v", code, err)
		}
	}
	if _, err := service.CreateDepartment(context.Background(), tenant,
		organisation.DepartmentEdit{Code: "sales", Name: "   "}); !errors.Is(err, organisation.ErrInvalidCode) {
		t.Fatalf("expected a blank name to be refused, got %v", err)
	}

	if _, err := service.CreateDepartment(context.Background(), tenant,
		organisation.DepartmentEdit{Code: "sales_2-b", Name: "Борлуулалт"}); err != nil {
		t.Fatalf("expected a catalogue-shaped code to be accepted: %v", err)
	}
	if _, err := service.CreateDepartment(context.Background(), tenant,
		organisation.DepartmentEdit{Code: "sales_2-b", Name: "Дахин"}); !errors.Is(err, organisation.ErrDuplicateCode) {
		t.Fatalf("expected a duplicate code to be refused, got %v", err)
	}

	// A rename does not go through the code rule: the code is what other things
	// refer to the unit by and is not editable here.
	if err := service.UpdateDepartment(context.Background(), tenant, "1",
		organisation.DepartmentEdit{Name: ""}); !errors.Is(err, organisation.ErrNameRequired) {
		t.Fatalf("expected a rename to nothing to be refused, got %v", err)
	}
}
