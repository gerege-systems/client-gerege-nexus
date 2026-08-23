package egov_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gerege-systems/client-gerege-nexus/domain/egov"
	"github.com/gerege-systems/client-gerege-nexus/domain/egov/memory"
)

const tenant = "tenant-1"

func newService(registry memory.Registry, rails egov.Rails, history memory.History) *egov.Service {
	return egov.NewService(registry, rails, history)
}

func registry() memory.Registry {
	return memory.Registry{
		Citizens:  map[string]egov.Citizen{"AA90010111": {RegNumber: "AA90010111", FirstName: "Бат"}},
		Companies: map[string]egov.Company{"5551234": {CompanyReg: "5551234", Name: "Гэрэгэ"}},
	}
}

// A lookup needs a subject, and the state decides what a valid one looks like.
func TestALookupNeedsANumberAndAnswersFromTheRegister(t *testing.T) {
	service := newService(registry(), nil, memory.History{})
	ctx := context.Background()

	for _, blank := range []string{"", "   "} {
		if _, err := service.Citizen(ctx, blank); !errors.Is(err, egov.ErrNoRegNumber) {
			t.Fatalf("a lookup with no number: %v", err)
		}
		if _, err := service.Company(ctx, blank); !errors.Is(err, egov.ErrNoCompanyReg) {
			t.Fatalf("a company lookup with no number: %v", err)
		}
	}

	citizen, err := service.Citizen(ctx, " AA90010111 ")
	if err != nil || citizen.FirstName != "Бат" {
		t.Fatalf("a padded number should still find the person: %+v %v", citizen, err)
	}
	if company, err := service.Company(ctx, "5551234"); err != nil || company.Name != "Гэрэгэ" {
		t.Fatalf("company: %+v %v", company, err)
	}

	// What the rail says is what the caller is told, because from the state's
	// side the message is the only part that says which of the two it was.
	unreachable := newService(memory.Registry{Err: errors.New("the endpoint is not configured")}, nil, memory.History{})
	_, err = unreachable.Citizen(ctx, "AA90010111")
	if err == nil || err.Error() != "XYP citizen query failed: the endpoint is not configured" {
		t.Fatalf("the rail's own words: %v", err)
	}
	if !egov.IsRefusal(err) {
		t.Fatal("a failed lookup has always been answered as the caller's mistake")
	}
}

// A screen that mentions a person's identities without saying where they are
// only sends people looking through Settings.
func TestConnectionsSayWhatIsWiredAndWhereIdentitiesLive(t *testing.T) {
	// A deployment wired to nothing is the ordinary case for this platform, and
	// it answers with a list rather than with nothing.
	bare := newService(registry(), nil, memory.History{}).Connections()
	if bare.Rails == nil || len(bare.Rails) != 0 {
		t.Fatalf("an unwired deployment should answer with an empty list, got %+v", bare.Rails)
	}
	if bare.IdentitiesPath != "/profile" {
		t.Fatalf("the connections screen must point at the person's own profile, got %q", bare.IdentitiesPath)
	}

	wired := newService(registry(), func() []egov.Rail {
		return []egov.Rail{{ID: "xyp", Name: "ХУР", Mode: "mock"}}
	}, memory.History{}).Connections()
	// A mock rail says so. Reporting it as connected is how a test fixture ends
	// up on a government form.
	if len(wired.Rails) != 1 || wired.Rails[0].Mode != "mock" {
		t.Fatalf("the rail's mode did not survive: %+v", wired.Rails)
	}
}

func TestTheHistoryIsTheAuditTrailAndSaysSoWhenItCannotBeRead(t *testing.T) {
	service := newService(registry(), nil, memory.History{
		ByTenant: map[string][]egov.Lookup{tenant: {{Action: egov.ActionCitizenQueried}}},
	})

	lookups, err := service.History(context.Background(), tenant)
	if err != nil || len(lookups) != 1 || lookups[0].Action != egov.ActionCitizenQueried {
		t.Fatalf("history: %+v %v", lookups, err)
	}
	if other, err := service.History(context.Background(), "somebody-else"); err != nil || len(other) != 0 {
		t.Fatalf("another organisation's history: %+v %v", other, err)
	}

	broken := newService(registry(), nil, memory.History{Err: errors.New("the connection is closed")})
	_, err = broken.History(context.Background(), tenant)
	if err == nil || egov.IsRefusal(err) || err.Error() != "could not load the history" {
		t.Fatalf("a trail that cannot be read is not the caller's mistake: %v", err)
	}
}
