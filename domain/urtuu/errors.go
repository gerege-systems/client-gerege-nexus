/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package urtuu

import (
	"errors"
	"fmt"

	contract "github.com/gerege-systems/client-gerege-nexus/domain/urtuu/wire"
)

// What the board refuses, in the words it has always used.
var (
	ErrAssignmentHasNoApplicant = errors.New("an assignment has no applicant; it is raised by this organisation")
	ErrServiceNeedsApplicant    = errors.New("a service request has to say who asked for it")
	ErrApplicantKind            = errors.New("an applicant is a citizen or an organisation")
	ErrNoAnswerForApplicant     = errors.New("a service request cannot be completed without an answer for the applicant")
	ErrNoBranches               = errors.New("delegating to nobody is not delegating")
	ErrReasonRequired           = errors.New("a reason is required")

	// ErrTransition is refusing a move the state machine does not allow. It is
	// a 409 rather than a 400 wherever it surfaces: the request was well formed
	// and the task is simply not where the caller thought it was — usually
	// because somebody else, or a subordinate's envelope, moved it first.
	ErrTransition = errors.New("that is not a move this task can make")
)

// CheckTransition is the state machine's guard, asked in the contract's own
// words. The table is contract's rather than a switch here, because the same
// table is what the transport, the app and the migration's CHECK are all held
// against — two expressions of a state machine drift; one does not.
func CheckTransition(from, to contract.TaskStatus) error {
	if from.CanMoveTo(to) {
		return nil
	}
	return fmt.Errorf("%w: %s → %s", ErrTransition, from, to)
}

// UnknownStatus and UnknownLine refuse a filter the machine does not know. A
// status nobody has would match nothing and read as "there is no work", which
// is the wrong answer to a typo.
func UnknownStatus(status string) error { return fmt.Errorf("no such status: %s", status) }

func UnknownLine(line string) error { return fmt.Errorf("no such line: %s", line) }

// CodeNotOpenOn refuses sending work under a code the link was never told
// about: the child would receive a task naming a code it has never heard of,
// and the whole point of announcing the vocabulary is that it does not have to
// guess.
func CodeNotOpenOn(code string) error {
	return fmt.Errorf("the code %s is not open on that link", code)
}
