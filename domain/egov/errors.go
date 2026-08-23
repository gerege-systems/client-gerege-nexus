/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package egov

import "errors"

// The refusals, with the words the handlers used.
var (
	ErrNoRegNumber  = rule("invalid registration number")
	ErrNoCompanyReg = rule("invalid company registration number")
)

// LookupFailed carries the rail's own complaint. A failed lookup is answered as
// the caller's mistake — a number that is not a number, an entity the register
// does not have — because from the state's side that is nearly always what it
// is, and the message is the only part that says which.
func LookupFailed(what string, err error) error {
	return rule("XYP " + what + " query failed: " + err.Error())
}

// Failure is a platform that could not answer. Not a refusal: nobody did
// anything wrong.
type Failure struct {
	Message string
	Err     error
}

func (e *Failure) Error() string { return e.Message }

func (e *Failure) Unwrap() error { return e.Err }

func Failed(message string, err error) error {
	var refusal *ruleError
	var failure *Failure
	if errors.As(err, &refusal) || errors.As(err, &failure) {
		return err
	}
	return &Failure{Message: message, Err: err}
}

// IsRefusal separates what the caller did from what failed underneath them.
func IsRefusal(err error) bool {
	var refusal *ruleError
	return errors.As(err, &refusal)
}

type ruleError struct{ message string }

func (e *ruleError) Error() string { return e.message }

func rule(message string) *ruleError { return &ruleError{message: message} }
