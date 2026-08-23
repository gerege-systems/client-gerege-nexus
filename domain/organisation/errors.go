/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package organisation

import (
	"errors"
	"fmt"
)

// The refusals this app makes, named.
//
// They used to be HTTP statuses written where the rule was decided, which made
// the rule and the way a browser hears about it the same line: to know that the
// last administrator cannot be deactivated you had to read a handler, and to
// test it you had to make a request. These are the rules themselves.
//
// The text is what those handlers sent, to the byte, because a refusal somebody
// has read on a screen is as much of the contract as the status beside it. The
// adapter writes err.Error() and picks the status from the sentinel, so a new
// rule needs one line here and one line in that map, and nothing else.
var (
	ErrSelfDeactivation  = ruleError("you cannot deactivate your own membership")
	ErrLastAdministrator = ruleError("this is the last administrator of the organisation; appoint another one first")
	// ErrCrossTenant is deliberately "not in your organisation" rather than a
	// refusal: whether this membership exists in some other tenant is not the
	// caller's business, and an answer that distinguished the two would say so.
	ErrCrossTenant       = ruleError("this person is not in your organisation")
	ErrForeignDepartment = ruleError("that department is not in your organisation")
	ErrForeignUnit       = ruleError("the parent department or manager is not in your organisation")
	ErrNotFound          = ruleError("department not found")
	ErrNameRequired      = ruleError("a department needs a name")
	ErrInvalidCode       = ruleError("a department needs a name and a code of lowercase letters, digits, - and _")
	ErrDuplicateCode     = ruleError("a department with that code already exists")
	ErrSelfParent        = ruleError("a department cannot report to itself")
	ErrCycle             = ruleError("that unit is already below this one; the two would report to each other")

	// The two rules whose refusal names what is in the way. The sentinel is
	// what callers match on; the message comes from the value below it.
	ErrParentArchived = ruleError("the unit this one reports to is archived")
	ErrUnitNotEmpty   = ruleError("this unit still has people or units under it")
)

// ArchivedParent refuses a restore and names the unit that has to come back
// first, because that is the whole of what the operator has to do next.
type ArchivedParent struct{ Name string }

func (e ArchivedParent) Error() string {
	return "the unit this one reports to is archived; restore " + e.Name + " first"
}

func (e ArchivedParent) Unwrap() error { return ErrParentArchived }

// NotEmpty counts what is in the way rather than only saying no: "3 people and
// 2 units report to this one" tells the operator what to move, and "nothing
// does" would not have been a refusal at all.
type NotEmpty struct{ People, Children int }

func (e NotEmpty) Error() string {
	return fmt.Sprintf(
		"this unit still has %d people and %d units under it; move them first, or archive it instead",
		e.People, e.Children)
}

func (e NotEmpty) Unwrap() error { return ErrUnitNotEmpty }

// Failure is storage that could not answer, carrying the sentence the operator
// should see. It is not a rule: nobody did anything wrong, the disk did.
//
// The message lives next to the call that failed rather than at the edge,
// because "could not check the administrators" and "could not save the change"
// are two different things to have failed and the handler above can no longer
// tell them apart — it makes one call now.
type Failure struct {
	Message string
	Err     error
}

func (e *Failure) Error() string { return e.Message }

func (e *Failure) Unwrap() error { return e.Err }

// Failed wraps a port's error, unless it is already something worth keeping.
//
// A refusal travels up untouched — a repository that answers ErrDuplicateCode
// has decided the rule, not failed at it — and a Failure that already carries a
// more precise sentence keeps it. That is how a scan error stays "could not
// read the people" while the query that produced no rows at all is "could not
// load the people".
func Failed(message string, err error) error {
	var rule ruleError
	var failure *Failure
	if errors.As(err, &rule) || errors.As(err, &failure) {
		return err
	}
	return &Failure{Message: message, Err: err}
}

// ruleError is what makes "a refusal" a thing the code can ask about. A plain
// errors.New would need every caller to list every sentinel to tell the two
// kinds apart.
type ruleError string

func (e ruleError) Error() string { return string(e) }
