package urtuu_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/urtuu"
	contract "github.com/gerege-systems/open-gerege-nexus/backend/pkg/urtuu"
)

// The board's judgements, run without two installations, a signing key or a
// migrated schema — which is what every one of them used to need.

const installation = "inst-here"

func code(sla int64) domain.RequestCode {
	return domain.RequestCode{
		Code:   "SVC-01",
		Names:  map[string]string{"mn": "Тодорхойлолт", "en": "Certificate"},
		SLA:    &sla,
		Line:   contract.LineService,
		Active: true,
	}
}

// A register number is what somebody quotes down a telephone, so its shape is
// part of the product rather than an implementation detail.
func TestARegisterNumberSaysWhichPromiseItIsUnder(t *testing.T) {
	if got := domain.FormatNumber(contract.LineService, 2026, 412); got != "Ү2026-00412" {
		t.Fatalf("a service number: %q", got)
	}
	if got := domain.FormatNumber(contract.LineAssignment, 2026, 1); got != "Д2026-00001" {
		t.Fatalf("an assignment number: %q", got)
	}
	// It grows rather than wrapping: an organisation past a hundred thousand
	// requests gets a longer number, not one somebody else already has.
	if got := domain.FormatNumber(contract.LineAssignment, 2026, 123456); got != "Д2026-123456" {
		t.Fatalf("a six-figure register: %q", got)
	}
	// An envelope from a build that predates the two lines can only be an
	// assignment, because that is the only thing it could raise.
	if got := domain.LineOf("something-else"); got != contract.LineAssignment {
		t.Fatalf("an unknown line: %q", got)
	}
	if got := domain.LineOf(contract.LineService); got != contract.LineService {
		t.Fatalf("a known line must survive: %q", got)
	}
}

// What a task is called is decided once, when it is raised: a code that is
// retranslated later must not rewrite what was asked for in March.
func TestATaskIsNamedWhenItIsRaised(t *testing.T) {
	given := code(0)
	if got := domain.TitleFor(given, "  Тусгай гарчиг  ", "mn"); got != "Тусгай гарчиг" {
		t.Fatalf("what the raiser typed wins: %q", got)
	}
	if got := domain.TitleFor(given, "", "en"); got != "Certificate" {
		t.Fatalf("the caller's language: %q", got)
	}
	// Mongolian is the fallback, and the code itself is the last resort — a
	// task with no name at all would be a row nobody can find.
	if got := domain.TitleFor(given, "", "fr"); got != "Тодорхойлолт" {
		t.Fatalf("the fallback: %q", got)
	}
	if got := domain.TitleFor(domain.RequestCode{Code: "X-1"}, "", "mn"); got != "X-1" {
		t.Fatalf("the last resort: %q", got)
	}
}

// The deadline is measured from the moment the work was promised against,
// which for received work is the sender's stamp rather than this
// installation's clock.
func TestADeadlineComesFromTheNormUnlessSomebodySaidOtherwise(t *testing.T) {
	stamped := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	due := domain.DeadlineFor(code(3600), nil, stamped)
	if due == nil || !due.Equal(stamped.Add(time.Hour)) {
		t.Fatalf("the code's norm from the sender's stamp: %v", due)
	}

	chosen := stamped.Add(48 * time.Hour)
	if got := domain.DeadlineFor(code(3600), &chosen, stamped); got == nil || !got.Equal(chosen) {
		t.Fatalf("an explicit deadline wins: %v", got)
	}
	// A code with no norm leaves the work undated rather than inventing one.
	if got := domain.DeadlineFor(domain.RequestCode{}, nil, stamped); got != nil {
		t.Fatalf("no norm, no deadline: %v", got)
	}
	if got := domain.DeadlineFor(code(0), nil, stamped); got != nil {
		t.Fatalf("a zero norm is no norm: %v", got)
	}
}

// A request with no applicant is one nobody can answer; an applicant on an
// internal order invents a citizen behind an instruction that had none.
func TestWhoAskedIsRequiredOnOneLineAndRefusedOnTheOther(t *testing.T) {
	citizen := &contract.Applicant{Kind: "citizen", Name: "Бат", RegistryNumber: "УБ99010111"}

	raw, err := domain.ApplicantFor(contract.LineService, citizen)
	if err != nil {
		t.Fatalf("a named citizen on the service line: %v", err)
	}
	var back contract.Applicant
	if err := json.Unmarshal(raw, &back); err != nil || back.Name != "Бат" {
		t.Fatalf("the applicant did not survive: %v %v", back, err)
	}

	if _, err := domain.ApplicantFor(contract.LineService, nil); !errors.Is(err, domain.ErrServiceNeedsApplicant) {
		t.Fatalf("a service request with nobody: %v", err)
	}
	if _, err := domain.ApplicantFor(contract.LineService, &contract.Applicant{Kind: "citizen"}); !errors.Is(err, domain.ErrServiceNeedsApplicant) {
		t.Fatalf("an applicant with no name is nobody: %v", err)
	}
	if _, err := domain.ApplicantFor(contract.LineService, &contract.Applicant{Kind: "робот", Name: "Бат"}); !errors.Is(err, domain.ErrApplicantKind) {
		t.Fatalf("an invented kind: %v", err)
	}
	if _, err := domain.ApplicantFor(contract.LineAssignment, citizen); !errors.Is(err, domain.ErrAssignmentHasNoApplicant) {
		t.Fatalf("an applicant on an assignment: %v", err)
	}
	// And the honest empty value on the line where nobody is waiting.
	empty, err := domain.ApplicantFor(contract.LineAssignment, nil)
	if err != nil || string(empty) != "{}" {
		t.Fatalf("an assignment's applicant column: %q %v", empty, err)
	}
}

// A service request completed with nothing to tell the person who asked is
// their question thrown away.
func TestAServiceRequestCannotBeCompletedWithNothingToTellTheApplicant(t *testing.T) {
	if err := domain.CanComplete(contract.LineService, "", "", ""); !errors.Is(err, domain.ErrNoAnswerForApplicant) {
		t.Fatalf("no answer anywhere: %v", err)
	}
	if err := domain.CanComplete(contract.LineService, "", "Олгов", ""); err != nil {
		t.Fatalf("an answer being given now: %v", err)
	}
	if err := domain.CanComplete(contract.LineService, "", "", "Өмнө нь олгосон"); err != nil {
		t.Fatalf("an answer already on the row: %v", err)
	}
	// An assignment has nobody outside the platform waiting.
	if err := domain.CanComplete(contract.LineAssignment, "", "", ""); err != nil {
		t.Fatalf("an assignment: %v", err)
	}
	// A mirror stands for work being done elsewhere; its answer arrives with
	// that installation's update.
	if err := domain.CanComplete(contract.LineService, "peer-1", "", ""); err != nil {
		t.Fatalf("a mirror row: %v", err)
	}
}

// А→Б→А is a legitimate peer graph, so it is the task that must not go round
// rather than the link that must not exist.
func TestWorkThatHasBeenHereBeforeIsRefusedWithAReason(t *testing.T) {
	chain := []string{"inst-ministry", installation, "inst-aimag"}
	if !domain.PassedThrough(chain, installation) {
		t.Fatal("a chain naming this installation is a cycle")
	}
	if domain.PassedThrough([]string{"inst-ministry"}, installation) {
		t.Fatal("a chain that has not been here is not a cycle")
	}

	refusals := []struct {
		name          string
		chain         []string
		code          domain.RequestCode
		found, failed bool
		want          string
	}{
		{"a cycle", chain, code(0), true, false,
			"this task has already passed through this installation (origin chain)"},
		{"a code nobody here has", nil, domain.RequestCode{}, false, false,
			"this installation has no request code SVC-01"},
		{"a code that was withdrawn", nil, domain.RequestCode{Active: false}, true, false,
			"the request code SVC-01 is not in use at this installation"},
		{"a lookup that failed", nil, domain.RequestCode{}, false, true,
			"this installation could not check the request code SVC-01"},
		{"nothing wrong", nil, code(0), true, false, ""},
	}
	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			got := domain.AssignmentRefusal(refusal.chain, installation, "SVC-01",
				refusal.code, refusal.found, refusal.failed)
			if got != refusal.want {
				t.Fatalf("got %q, want %q", got, refusal.want)
			}
		})
	}

	// A database that was down is not a code that does not exist, and the
	// cycle is checked before either — it needs no lookup at all.
	if got := domain.AssignmentRefusal(chain, installation, "SVC-01", domain.RequestCode{}, false, true); got !=
		"this task has already passed through this installation (origin chain)" {
		t.Fatalf("the cycle is decided first: %q", got)
	}
}

// Deliveries retry and can arrive out of order; an older update must not walk
// a finished task backwards.
func TestAnOlderUpdateDoesNotUndoAFinishedTask(t *testing.T) {
	if domain.SupersededBy(string(contract.StatusCompleted), string(contract.StatusAccepted)) {
		t.Fatal("an accept arriving after a completion must be ignored")
	}
	if !domain.SupersededBy(string(contract.StatusAccepted), string(contract.StatusCompleted)) {
		t.Fatal("a completion after an accept is news")
	}
	// The same status twice is a retry, not a transition, and writes no event.
	if domain.SupersededBy(string(contract.StatusAccepted), string(contract.StatusAccepted)) {
		t.Fatal("a repeat delivery must not write a second event")
	}
	// RETURNED and COMPLETED are both answers and rank together: whichever
	// arrives second is still an answer and is applied.
	if !domain.SupersededBy(string(contract.StatusCompleted), string(contract.StatusReturned)) {
		t.Fatal("a return after a completion is the subordinate changing its answer")
	}
	// A mirror is somebody else's state, so the transition table does not
	// govern it — but the task this side owns is governed.
	if err := domain.CheckTransition(contract.StatusReceived, contract.StatusClosed); err == nil {
		t.Fatal("RECEIVED → CLOSED is not a move this task can make")
	}
	if err := domain.CheckTransition(contract.StatusReceived, contract.StatusAccepted); err != nil {
		t.Fatalf("RECEIVED → ACCEPTED: %v", err)
	}
}

// The person who asked is entitled to every office's answer, and to know which
// office gave which.
func TestTheBranchesAnswersAreJoinedAndNamed(t *testing.T) {
	one := domain.JoinAnswers([]domain.BranchAnswer{{Peer: "Багануур", Answer: "Олгов"}})
	if one != "Олгов" {
		t.Fatalf("one office answers as itself: %q", one)
	}
	many := domain.JoinAnswers([]domain.BranchAnswer{
		{Peer: "Багануур", Answer: "Олгов"},
		{Peer: "Налайх", Answer: "Бүртгэл олдсонгүй"},
	})
	if many != "Багануур: Олгов\nНалайх: Бүртгэл олдсонгүй" {
		t.Fatalf("two offices, two answers: %q", many)
	}
	// A branch that said nothing is not an answer.
	if got := domain.JoinAnswers([]domain.BranchAnswer{{Peer: "Багануур", Answer: "  "}}); got != "" {
		t.Fatalf("silence is not an answer: %q", got)
	}
	if got := domain.JoinAnswers(nil); got != "" {
		t.Fatalf("no branches: %q", got)
	}
}

func TestWhereATaskFacesAndWhereItStarts(t *testing.T) {
	if got := domain.DirectionOf("peer-1", ""); got != domain.DirectionIncoming {
		t.Fatalf("work this organisation owes somebody: %q", got)
	}
	if got := domain.DirectionOf("", "peer-2"); got != domain.DirectionOutgoing {
		t.Fatalf("work it is owed: %q", got)
	}
	if got := domain.DirectionOf("", ""); got != domain.DirectionLocal {
		t.Fatalf("its own: %q", got)
	}

	// A task with branches is delegated from birth. Neither is a transition.
	if got := domain.InitialStatus(0); got != contract.StatusReceived {
		t.Fatalf("a task kept here: %q", got)
	}
	if got := domain.InitialStatus(21); got != contract.StatusDelegated {
		t.Fatalf("a fan-out: %q", got)
	}

	// A NOT NULL JSON column takes `{}` rather than null for "nobody filled
	// this in".
	if got := string(domain.PayloadOrEmpty(nil)); got != "{}" {
		t.Fatalf("an empty payload: %q", got)
	}
	if got := string(domain.PayloadOrEmpty(json.RawMessage(`{"a":1}`))); got != `{"a":1}` {
		t.Fatalf("a payload must survive: %q", got)
	}
}
