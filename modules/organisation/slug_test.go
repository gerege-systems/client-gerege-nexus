package organisation_test

import (
	"context"
	"strings"
	"testing"

	org "github.com/gerege-systems/client-gerege-nexus/domain/organisation"
	"github.com/gerege-systems/client-gerege-nexus/domain/organisation/memory"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/catalog"
)

// The domain carries its own copy of catalog.IsValidSlug, because pkg/catalog
// imports pkg/nexus and calling it would put the platform SDK back inside a
// package whose whole point is not to have one.
//
// A copy drifts unless something is watching. This is the something, and it
// lives here because the adapter is the one place that may import both: the
// domain by design, the catalogue because it is on the platform side of the
// line. If the two ever disagree, this fails — rather than a code being
// accepted by a department and refused by everything else that reads a slug.
func TestTheDomainAgreesWithTheCatalogueAboutCodes(t *testing.T) {
	codes := []string{
		"", "sales", "sales-2", "sales_2", "SALES", "Sales", "sales dept",
		"sales.dept", "sales/../etc", "борлуулалт", "販売", "sales\n", " sales",
		"a", strings.Repeat("a", 64), strings.Repeat("a", 65), "-", "_", "0",
	}
	for _, code := range codes {
		// The service trims before it validates, so the catalogue is asked the
		// same question the domain ends up asking.
		trimmed := strings.TrimSpace(code)
		// A store per code: a second department with a code already taken is
		// refused for a different reason, and this is not asking about that.
		service := org.NewService(memory.New())
		_, err := service.CreateDepartment(context.Background(), "tenant",
			org.DepartmentEdit{Code: code, Name: "Нэгж"})
		refused := err != nil
		if refused != !catalog.IsValidSlug(trimmed) {
			t.Errorf("the domain and the catalogue disagree about %q: catalogue says valid=%v, the domain answered %v",
				code, catalog.IsValidSlug(trimmed), err)
		}
	}
}
