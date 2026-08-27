package reports_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gerege-systems/client-gerege-nexus/domain/reports"
	"github.com/gerege-systems/client-gerege-nexus/domain/reports/memory"
)

// The rules of the reports app, run without PostgreSQL and without the
// reporting engine.
//
// Every one of them used to need two tenants, a migrated schema and an HTTP
// request to observe — which is why, until the app got its first test, none of
// them was observed at all.

const (
	us      = "tenant-us"
	them    = "tenant-them"
	ourReg  = "REG-US"
	theirRe = "REG-THEM"

	ourApp   = "io.gerege.nexus.reports"
	otherApp = "io.gerege.test.absent"

	shareable   = "test.shared"
	unshareable = "test.plain"
	elsewhere   = "test.absent"
)

func newService(t *testing.T, catalogue memory.Catalogue) (*reports.Service, *memory.Store) {
	t.Helper()
	store := memory.New()
	store.Register(us, ourReg)
	store.Register(them, theirRe)
	installed := func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{ourApp: true}, nil
	}
	return reports.NewService(catalogue, store, installed), store
}

func catalogue() memory.Catalogue {
	return memory.Catalogue{Reports: map[string]reports.Report{
		shareable: {Key: shareable, App: ourApp, Titles: map[string]string{"mn": "Хуваалцах"},
			Scopes: []string{reports.ScopeCounterparty, reports.ScopeFull}},
		unshareable: {Key: unshareable, App: ourApp, Titles: map[string]string{"mn": "Энгийн"}},
		elsewhere:   {Key: elsewhere, App: otherApp, Titles: map[string]string{"mn": "Өөр аппынх"}},
	}}
}

func schedule() reports.ScheduleEdit {
	return reports.ScheduleEdit{
		ReportKey:  shareable,
		Cron:       "0 6 1 * *",
		Format:     "xlsx",
		Recipients: []string{"a@example.com"},
	}
}

// The app gate, which is the check a caller who knows a key gets past when it
// is left out.
func TestOnlyTheReportsOfInstalledAppsAreVisible(t *testing.T) {
	service, _ := newService(t, catalogue())
	ctx := context.Background()

	groups, err := service.Available(ctx, us, "mn")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].App != ourApp || len(groups[0].Reports) != 2 {
		t.Fatalf("the list should hold this organisation's app and nothing else: %+v", groups)
	}

	if _, err := service.Resolve(ctx, us, shareable); err != nil {
		t.Fatalf("an installed app's report: %v", err)
	}
	// A report of an app this organisation does not have, and a key that is
	// nothing at all, answer the same way — anything else enumerates the
	// catalogue.
	for _, key := range []string{elsewhere, "nope.at.all"} {
		_, err := service.Resolve(ctx, us, key)
		if !errors.Is(err, reports.ErrReportUnavailable) {
			t.Fatalf("%s: %v", key, err)
		}
		if err.Error() != "no such report" {
			t.Fatalf("the refusal changed its words: %q", err)
		}
	}

	// Named in a body rather than in the path, the same report is a bad field
	// rather than a missing page — the words are the same and the value is not.
	_, err = service.CreateSchedule(ctx, us, reports.ScheduleEdit{ReportKey: elsewhere, Cron: "0 6 1 * *"})
	if !errors.Is(err, reports.ErrNoSuchReport) || errors.Is(err, reports.ErrReportUnavailable) {
		t.Fatalf("a schedule naming another app's report: %v", err)
	}
}

// A schedule runs weeks later with nobody present, so everything wrong with it
// has to be said now.
func TestAScheduleIsCheckedBeforeItIsStored(t *testing.T) {
	service, store := newService(t, catalogue())
	ctx := context.Background()

	refused := func(name string, edit reports.ScheduleEdit, want error) {
		t.Helper()
		_, err := service.CreateSchedule(ctx, us, edit)
		if !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", name, err, want)
		}
	}

	none := schedule()
	none.Recipients = nil
	refused("no recipients", none, reports.ErrNoRecipients)

	crowd := schedule()
	crowd.Recipients = make([]string, 21)
	for i := range crowd.Recipients {
		crowd.Recipients[i] = "a@example.com"
	}
	refused("twenty-one recipients", crowd, reports.ErrTooManyPeople)

	unknown := schedule()
	unknown.ReportKey = "nope"
	refused("unknown report", unknown, reports.ErrNoSuchReport)

	// The one that names what is wrong rather than only that something is.
	bad := schedule()
	bad.Recipients = []string{"a@example.com", "not an address"}
	if _, err := service.CreateSchedule(ctx, us, bad); err == nil ||
		err.Error() != "not an address is not an e-mail address" {
		t.Fatalf("the refusal should name the entry: %v", err)
	}

	// The engine's own words are forwarded rather than restated.
	engineSays := catalogue()
	engineSays.CronError = errors.New("a schedule expression has five fields")
	refusing, _ := newService(t, engineSays)
	if _, err := refusing.CreateSchedule(ctx, us, schedule()); err == nil ||
		err.Error() != "the schedule expression is not valid: a schedule expression has five fields" {
		t.Fatalf("the cron refusal: %v", err)
	}

	// And what a good one is stored as: lowercased, de-duplicated, active
	// unless somebody said otherwise.
	good := schedule()
	good.Name = "  Сар бүр  "
	good.Recipients = []string{"A@Example.com", "a@example.com", " b@example.com "}
	stored, err := service.CreateSchedule(reports.WithActor(ctx, "user-1"), us, good)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if strings.Join(stored.Recipients, ",") != "a@example.com,b@example.com" {
		t.Fatalf("the address list was not cleaned up: %v", stored.Recipients)
	}
	if !stored.Active || stored.Name != "Сар бүр" || stored.CreatedBy != "user-1" {
		t.Fatalf("stored wrong: %+v", stored)
	}
	if _, found := store.Schedule(stored.ID); !found {
		t.Fatal("the schedule was not stored")
	}

	// Another organisation's schedule is not there to be edited or removed.
	if _, err := service.UpdateSchedule(ctx, them, stored.ID, schedule()); !errors.Is(err, reports.ErrNoSuchSchedule) {
		t.Fatalf("editing across organisations: %v", err)
	}
	if _, err := service.DeleteSchedule(ctx, them, stored.ID); !errors.Is(err, reports.ErrNoSuchSchedule) {
		t.Fatalf("removing across organisations: %v", err)
	}
	if key, err := service.DeleteSchedule(ctx, us, stored.ID); err != nil || key != shareable {
		t.Fatalf("delete answered %q, %v", key, err)
	}
}

// Sharing is the only thing this app does that crosses an organisation's
// boundary, so every refusal in it is load-bearing.
func TestOnlyTheOwnerAgreesAndEitherSideMayEnd(t *testing.T) {
	service, _ := newService(t, catalogue())
	ctx := reports.WithActor(context.Background(), "user-1")

	ask := reports.GrantRequest{GrantorRegistrationNumber: theirRe, ReportKey: shareable, Scope: reports.ScopeFull}

	refused := func(name string, request reports.GrantRequest, want error) {
		t.Helper()
		_, err := service.RequestGrant(ctx, us, request)
		if !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", name, err, want)
		}
	}

	unknownOrg := ask
	unknownOrg.GrantorRegistrationNumber = "REG-NOBODY"
	refused("unknown organisation", unknownOrg, reports.ErrNoSuchTenant)

	itself := ask
	itself.GrantorRegistrationNumber = ourReg
	refused("asking itself", itself, reports.ErrSelfRequest)

	invented := ask
	invented.Scope = "everything"
	refused("invented scope", invented, reports.ErrInvalidScope)

	plain := ask
	plain.ReportKey = unshareable
	refused("a report never written to be shared", plain, reports.ErrUnshareable)

	badDate := ask
	badDate.ValidUntil = "01.01.2027"
	refused("a date nobody can parse", badDate, reports.ErrInvalidValidTo)

	// Counterparty scope needs the asking organisation to be identifiable, or
	// the grant would point at nothing.
	anonymous, store := newService(t, catalogue())
	store.Register(us, "")
	counterparty := ask
	counterparty.Scope = reports.ScopeCounterparty
	if _, err := anonymous.RequestGrant(ctx, us, counterparty); !errors.Is(err, reports.ErrNoRegistration) {
		t.Fatalf("an organisation with no registration number: %v", err)
	}

	grant, err := service.RequestGrant(ctx, us, ask)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if grant.GrantorWorkspaceID != them || grant.CreatedBy != "user-1" {
		t.Fatalf("the request was recorded wrong: %+v", grant)
	}
	// The default scope is the narrow one, and the counterparty reference is
	// decided once and stored.
	byDefault, _ := newService(t, catalogue())
	defaulted, err := byDefault.RequestGrant(ctx, us, reports.GrantRequest{
		GrantorRegistrationNumber: theirRe, ReportKey: shareable})
	if err != nil || defaulted.Scope != reports.ScopeCounterparty || defaulted.CounterpartyRef != ourReg {
		t.Fatalf("the default scope: %+v %v", defaulted, err)
	}

	refused("a second live request", ask, reports.ErrGrantExists)

	// The asking side cannot agree on the owner's behalf, which is the whole of
	// what accepting checks.
	if _, err := service.AcceptGrant(ctx, us, grant.ID); !errors.Is(err, reports.ErrNoPendingRequest) {
		t.Fatalf("the grantee accepting its own request: %v", err)
	}
	if key, err := service.AcceptGrant(ctx, them, grant.ID); err != nil || key != shareable {
		t.Fatalf("the owner accepting: %q %v", key, err)
	}
	if _, err := service.AcceptGrant(ctx, them, grant.ID); !errors.Is(err, reports.ErrNoPendingRequest) {
		t.Fatalf("accepting twice: %v", err)
	}

	// Either side may end it, and which side did is what the audit records.
	key, side, err := service.RevokeGrant(ctx, us, grant.ID)
	if err != nil || key != shareable || side != reports.SideReceived {
		t.Fatalf("the reader withdrawing: %q %q %v", key, side, err)
	}
	if _, _, err := service.RevokeGrant(ctx, us, grant.ID); !errors.Is(err, reports.ErrNoSuchGrant) {
		t.Fatalf("revoking twice: %v", err)
	}

	// And once it is ended the same request can be made again — the index is
	// on live agreements, not on history.
	again, err := service.RequestGrant(ctx, us, ask)
	if err != nil {
		t.Fatalf("asking again after a revoke: %v", err)
	}
	if _, _, err := service.RevokeGrant(ctx, them, again.ID); err != nil {
		t.Fatalf("the owner ending it: %v", err)
	}
}

// A refusal from a port that fails is not a refusal: nobody did anything wrong.
func TestAPlatformThatCannotAnswerIsNotARefusal(t *testing.T) {
	store := memory.New()
	broken := func(context.Context, string) (map[string]bool, error) {
		return nil, errors.New("the installation cache is down")
	}
	service := reports.NewService(catalogue(), store, broken)

	_, err := service.Available(context.Background(), us, "mn")
	if err == nil || reports.IsRefusal(err) {
		t.Fatalf("an unreachable platform should not read as the caller's mistake: %v", err)
	}
	if err.Error() != "could not check the installed apps" {
		t.Fatalf("the operator's sentence: %q", err)
	}
}

// brokenStore answers every lookup and fails the one write, which is the shape
// of a database that has gone away mid-request.
type brokenStore struct{ *memory.Store }

func (brokenStore) CreateGrant(context.Context, reports.Grant) (string, error) {
	return "", errors.New("the connection is closed")
}

// A database that is down is not a colleague who has already asked.
//
// The handler this rule came from answered 409 — "a request or agreement for
// this report already exists between these organisations" — to every failure of
// the insert, so an operator whose database had gone away was told a story
// about their own organisation instead.
func TestAStoreThatFailsIsNotAConflict(t *testing.T) {
	store := memory.New()
	store.Register(us, ourReg)
	store.Register(them, theirRe)
	installed := func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{ourApp: true}, nil
	}
	service := reports.NewService(catalogue(), brokenStore{store}, installed)

	_, err := service.RequestGrant(context.Background(), us, reports.GrantRequest{
		GrantorRegistrationNumber: theirRe, ReportKey: shareable, Scope: reports.ScopeFull})
	if err == nil || reports.IsRefusal(err) || errors.Is(err, reports.ErrGrantExists) {
		t.Fatalf("a store that failed should not read as a conflict: %v", err)
	}
	if err.Error() != "could not record the request" {
		t.Fatalf("the operator's sentence: %q", err)
	}
}
