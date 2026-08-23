/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package reports

import "errors"

// The refusals this app makes, named. The text is what the handlers sent, to
// the byte: the adapter writes err.Error() and takes the status from the
// sentinel.
var (
	// ErrNoSuchReport is a report named in a body — a schedule's report_key, a
	// grant's — that this organisation cannot see. It is a 400, because the
	// caller sent a field that is wrong.
	ErrNoSuchReport = rule("no such report")
	// ErrReportUnavailable is the same sentence about a report named in the
	// path, and it is a 404. The two are deliberately separate values with the
	// same words: what the caller did wrong differs, and what they are told
	// must not — whether an app exists on this deployment is not their
	// business, and a 403 here plus a 404 there would enumerate the catalogue.
	ErrReportUnavailable = rule("no such report")

	ErrInvalidScope     = rule("scope must be counterparty or full")
	ErrUnshareable      = rule("this report cannot be shared with that scope")
	ErrSelfRequest      = rule("an organisation cannot ask itself for a report")
	ErrNoRegistration   = rule("your organisation has no registration number; set it in Organisation before asking for a counterparty report")
	ErrInvalidValidTo   = rule("valid_until must be YYYY-MM-DD")
	ErrNoSuchTenant     = rule("no organisation with that registration number")
	ErrGrantExists      = rule("a request or agreement for this report already exists between these organisations")
	ErrNoPendingRequest = rule("no such pending request")
	ErrNoSuchGrant      = rule("no such agreement")

	ErrNoRecipients   = rule("a schedule needs at least one recipient")
	ErrTooManyPeople  = rule("a schedule may not have more than twenty recipients")
	ErrNoSuchSchedule = rule("no such schedule")
)

// NotAnAddress names the entry that could not be read, because "one of these
// twenty is wrong" is not something anybody can act on.
func NotAnAddress(given string) error { return rule(given + " is not an e-mail address") }

// Refused carries a refusal whose words came from somewhere else — the cron
// parser, the format list, the parameter binder. Those messages are the
// engine's and are already what the caller saw; repeating them here in worse
// words would be a change nobody asked for.
func Refused(message string) error { return rule(message) }

// Failure is storage or a platform call that could not answer, carrying the
// sentence the operator should see. It is not a refusal: nobody did anything
// wrong.
type Failure struct {
	Message string
	Err     error
}

func (e *Failure) Error() string { return e.Message }

func (e *Failure) Unwrap() error { return e.Err }

// Failed wraps an error from a port, unless it is already a refusal or a
// Failure that carries a more precise sentence.
func Failed(message string, err error) error {
	var refusal *ruleError
	var failure *Failure
	if errors.As(err, &refusal) || errors.As(err, &failure) {
		return err
	}
	return &Failure{Message: message, Err: err}
}

// IsRefusal reports whether an error is something the caller did rather than
// something that failed. The adapter turns the first into a 4xx and the second
// into a 500.
func IsRefusal(err error) bool {
	var refusal *ruleError
	return errors.As(err, &refusal)
}

// ruleError is a pointer type on purpose. Two refusals here carry identical
// words — see ErrNoSuchReport and ErrReportUnavailable — and a comparable
// string type would make errors.Is answer true for both, which is exactly the
// distinction the status map depends on.
type ruleError struct{ message string }

func (e *ruleError) Error() string { return e.message }

func rule(message string) *ruleError { return &ruleError{message: message} }
