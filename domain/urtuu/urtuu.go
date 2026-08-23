/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package urtuu holds the task board's decisions, with no database and no
 * channel under them.
 *
 * The channel is the platform's — links, signatures, queues, retries — and the
 * SQL is the app's. What is here is everything in between that is only ever a
 * judgement: what a task is called, when it is due, which promise it is under,
 * whether an installation may take it at all, and what a subordinate's news is
 * allowed to do to the copy this side keeps.
 *
 * Every one of these used to need two installations, a migrated schema and a
 * signing key to observe. They are the sentences somebody says out loud about
 * Өртөө, so they are here, in stdlib Go plus the wire contract every
 * installation on the ring already shares.
 */
package urtuu

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	contract "github.com/gerege-systems/open-gerege-nexus/backend/pkg/urtuu"
)

// RequestCode is what the board needs to know about a code before raising work
// under it.
type RequestCode struct {
	Code   string
	Names  map[string]string
	SLA    *int64
	Line   string
	Active bool
	Source string
}

// The three directions a task can face, derived from which side of a link it
// came from rather than stored: "incoming" is work this organisation owes
// somebody, "outgoing" is work it is owed, "local" is its own.
const (
	DirectionIncoming = "incoming"
	DirectionOutgoing = "outgoing"
	DirectionLocal    = "local"
)

// DirectionOf is one field the screens can filter on instead of two nullable
// ids they would each have to interpret.
func DirectionOf(originPeerID, targetPeerID string) string {
	switch {
	case originPeerID != "":
		return DirectionIncoming
	case targetPeerID != "":
		return DirectionOutgoing
	default:
		return DirectionLocal
	}
}

// LineOf keeps an envelope from an older build readable. A peer that does not
// know about the two lines can only be sending assignments, because that is the
// only thing its build could raise.
func LineOf(line string) string {
	if contract.KnownLine(line) {
		return line
	}
	return contract.LineAssignment
}

// LineMark is the first character of a register number: the promise, in one
// letter.
//
// Cyrillic, because the number is printed on Mongolian paperwork and read by
// people who call the two lines Үйлчилгээ and Даалгавар. It is never an API
// identifier — routes take a UUID — so it does not have to survive a URL.
func LineMark(line string) string {
	if line == contract.LineService {
		return "Ү"
	}
	return "Д"
}

// FormatNumber is what a person quotes down a telephone: "Д2026-00412".
//
// Five digits, and it grows rather than wrapping: an organisation that passes a
// hundred thousand requests in a year gets a longer number, not a number
// somebody else already has.
//
// The year is the caller's to decide, and it is the year of *registration* in
// the installation's own timezone — a request that arrives on the 31st of
// December and is registered on the 1st of January belongs to the new year's
// register.
func FormatNumber(line string, year, sequence int) string {
	return fmt.Sprintf("%s%d-%05d", LineMark(line), year, sequence)
}

// TitleFor is what a task is called, decided once when it is raised.
//
// Copied rather than looked up on every read: a code can be withdrawn or
// retranslated, and what was asked for in March has to still read as what was
// asked for in March.
func TitleFor(code RequestCode, given, locale string) string {
	if title := strings.TrimSpace(given); title != "" {
		return title
	}
	if name := strings.TrimSpace(code.Names[locale]); name != "" {
		return name
	}
	if name := strings.TrimSpace(code.Names["mn"]); name != "" {
		return name
	}
	return code.Code
}

// DeadlineFor resolves when the work is due: from the code's norm when nobody
// said otherwise, measured from the moment the task is raised — which for
// received work is the sender's stamp rather than this installation's clock.
func DeadlineFor(code RequestCode, given *time.Time, from time.Time) *time.Time {
	if given != nil {
		return given
	}
	if code.SLA == nil || *code.SLA <= 0 {
		return nil
	}
	due := from.Add(time.Duration(*code.SLA) * time.Second)
	return &due
}

// InitialStatus is where a newly raised task starts.
//
// A task with branches is delegated from birth; one kept here starts where
// every task starts. Neither is a transition — this is the initial state, which
// is why it is written rather than moved to.
func InitialStatus(branches int) contract.TaskStatus {
	if branches > 0 {
		return contract.StatusDelegated
	}
	return contract.StatusReceived
}

// ApplicantFor validates who is said to have asked, and renders them for a NOT
// NULL column.
//
// Required on the service line and refused on the assignment line. Both halves
// matter: a request with no applicant is one nobody can answer, and an
// applicant attached to a ministry's internal order invents a citizen behind an
// instruction that had none.
func ApplicantFor(line string, given *contract.Applicant) ([]byte, error) {
	if line != contract.LineService {
		if given != nil && given.Named() {
			return nil, ErrAssignmentHasNoApplicant
		}
		// The column is NOT NULL DEFAULT '{}', and empty is the honest value
		// for a line where nobody outside the platform is waiting.
		return []byte(`{}`), nil
	}
	if given == nil || !given.Named() {
		return nil, ErrServiceNeedsApplicant
	}
	if given.Kind != "citizen" && given.Kind != "organisation" {
		return nil, ErrApplicantKind
	}
	return json.Marshal(given)
}

// CanComplete is the service line's whole promise.
//
// A service request completed with nothing to tell the person who asked is
// their question thrown away. The database refuses it too — a check in one
// handler is a check that can be gone round — but the caller deserves a
// sentence rather than a constraint violation.
//
// A mirror row is exempt: it stands for work being done at another
// installation, and the answer arrives with that installation's update.
func CanComplete(line, targetPeerID, incomingAnswer, storedAnswer string) error {
	if line != contract.LineService || targetPeerID != "" {
		return nil
	}
	if strings.TrimSpace(incomingAnswer) != "" || strings.TrimSpace(storedAnswer) != "" {
		return nil
	}
	return ErrNoAnswerForApplicant
}

// PassedThrough is the cycle guard.
//
// А→Б→А is a legitimate shape for a peer graph — two ministries can each be
// above the other for different kinds of work — so it is the *task* that must
// not go round, not the link that must not exist.
func PassedThrough(chain []string, installationID string) bool {
	for _, installation := range chain {
		if installation == installationID {
			return true
		}
	}
	return false
}

// AssignmentRefusal is the reason an incoming task cannot be taken, or empty.
//
// Every refusal is answered upward rather than dropped: a parent that is told
// why can fix it, and a parent that is told nothing waits for ever. A lookup
// that failed is refused too — but with its own words, because a database that
// was down is not a code that does not exist, and the parent has to be able to
// tell those apart when it retries.
func AssignmentRefusal(chain []string, installationID, wantedCode string,
	code RequestCode, found, lookupFailed bool) string {

	if PassedThrough(chain, installationID) {
		return "this task has already passed through this installation (origin chain)"
	}
	if lookupFailed {
		return "this installation could not check the request code " + wantedCode
	}
	if !found {
		return "this installation has no request code " + wantedCode
	}
	if !code.Active {
		return "the request code " + wantedCode + " is not in use at this installation"
	}
	return ""
}

// Rank orders the statuses for the monotonicity check on incoming updates. It
// is not part of the contract's state machine and must not be mistaken for one:
// it exists only so that two updates arriving out of order settle the same way
// round.
func Rank(status string) int {
	switch contract.TaskStatus(status) {
	case contract.StatusReceived:
		return 0
	case contract.StatusAccepted:
		return 1
	case contract.StatusInProgress, contract.StatusDelegated:
		return 2
	case contract.StatusCompleted, contract.StatusReturned:
		return 3
	case contract.StatusClosed:
		return 4
	default:
		return -1
	}
}

// SupersededBy reports whether a subordinate's news should be applied to the
// mirror this side keeps.
//
// A mirror is not a state machine this side owns — it is somebody else's state
// copied here — so the transition table is not applied to it. What is applied
// is monotonicity: deliveries retry and can arrive out of order, and an older
// update must not walk a finished task backwards.
func SupersededBy(current, incoming string) bool {
	if incoming == current {
		return false
	}
	return Rank(incoming) >= Rank(current)
}

// BranchAnswer is one subordinate's reply, for the roll-up.
type BranchAnswer struct {
	// Peer is the office that answered, as it is named on the link.
	Peer   string
	Answer string
}

// JoinAnswers is what the branches said, as the applicant reads it.
//
// One branch answers as itself; several are joined and each is named, because
// "the certificate was issued" from one office and "no record found" from
// another are two different answers to the same request and the person who
// asked is entitled to both.
func JoinAnswers(branches []BranchAnswer) string {
	parts := make([]string, 0, len(branches))
	for _, branch := range branches {
		if strings.TrimSpace(branch.Answer) == "" {
			continue
		}
		parts = append(parts, branch.Peer+": "+branch.Answer)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		// The office's name only helps when there is more than one of them.
		if _, rest, found := strings.Cut(parts[0], ": "); found {
			return rest
		}
		return parts[0]
	default:
		return strings.Join(parts, "\n")
	}
}

// PayloadOrEmpty and ApplicantOrEmpty render a JSON column that is NOT NULL.
// Absent is `{}` rather than null: the column refuses null, and `{}` is what a
// form nobody filled in actually is.
func PayloadOrEmpty(raw json.RawMessage) []byte { return orEmptyObject(raw) }

func ApplicantOrEmpty(raw json.RawMessage) []byte { return orEmptyObject(raw) }

func orEmptyObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}
