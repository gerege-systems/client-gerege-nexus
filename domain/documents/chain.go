/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Package documents holds the approval chain's rules: who a document has to be
 * signed by, in what order, and which chains can never be completed.
 *
 * Not the app. Storing a document, filing it, pushing an eID ceremony to a
 * phone and rendering a signed PDF are the platform's rails and the app's SQL,
 * and both stay where they are — the same call the reports app made about the
 * reporting engine. What is here is the half that is only ever a decision, and
 * every one of these decisions used to need a migrated database and a signing
 * provider to observe:
 *
 *   - one citizen signs a document once, so a chain naming the same person
 *     twice can never be completed;
 *   - a step naming something no provider would vouch for is a step nobody can
 *     fill;
 *   - a policy that insists on named signers, applied to a chain with an open
 *     step, leaves the type unapprovable by anybody.
 *
 * They are counted in runes rather than bytes and upper-cased in Go rather than
 * in SQL, and both of those are load-bearing — see the comments where they are
 * done.
 */
package documents

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// WorkflowStep is one approval a document type needs, in order. An empty
// SignerRegNumber means the step counts but names nobody for it.
type WorkflowStep struct {
	Order           int    `json:"order"`
	Name            string `json:"name"`
	SignerRegNumber string `json:"signer_reg_number"`
}

// DocumentWorkflow is the ordered approval chain for one document type. No steps
// means a single signature approves, which is how the app behaved before.
type DocumentWorkflow struct {
	DocType string         `json:"doc_type"`
	Steps   []WorkflowStep `json:"steps"`
}

// ApprovalStep is one step of a document's OWN chain — the copy taken when it
// started waiting for approval, which no later configuration change touches.
type ApprovalStep struct {
	Order int    `json:"order"`
	Name  string `json:"name"`
	// Empty means the step is open: anyone allowed to sign may take it.
	SignerRegNumber string `json:"signer_reg_number"`
}

const (
	// RegNumberLimit is the shortest thing that can be a registration number.
	// Both national channels refuse anything shorter, so naming one in an
	// approval chain would create a step no citizen could fill.
	RegNumberLimit = 8
	// RegNumberMax is what document_workflow_steps.signer_reg_number holds, in
	// the characters Postgres counts.
	RegNumberMax = 64
	// MaxChainSteps is as long as a chain may be.
	MaxChainSteps = 10
)

// NormaliseRegNumber is the one shape a registration number is compared in.
// Every decision that pairs a number with another number — whether a named step
// is this citizen's, whether they have signed already, the ledger's
// one-per-signer constraint — rests on both sides having been through here.
//
// It is deliberately Go rather than SQL. Postgres upper() is governed by the
// database's ctype: on a cluster initialised with LC_CTYPE=C,
// upper('уб99010111') is 'уб99010111' unchanged, while Go upper-cases Cyrillic
// wherever it runs. Since Mongolian registration numbers are Cyrillic, letting
// SQL decide would make the feature's central comparison depend on how somebody
// ran initdb.
func NormaliseRegNumber(reg string) string {
	return strings.ToUpper(strings.TrimSpace(reg))
}

// PlausibleRegNumber reports whether a normalised number could be presented by a
// citizen at all.
//
// Counted in RUNES, not bytes. A Mongolian registration number is Cyrillic —
// 'УБ99010111' is 10 characters in 20 bytes — and the column is VARCHAR(64),
// which Postgres also counts in characters. Measuring bytes here made this check
// disagree with both the column and the SQL that repairs stored chains:
// 'УБ9901' is 8 bytes but 6 characters, so it was stored as a named step and
// then copied onto every document as an open one, leaving the screen naming a
// citizen the document's own chain did not.
func PlausibleRegNumber(reg string) bool {
	n := utf8.RuneCountInString(reg)
	return n >= RegNumberLimit && n <= RegNumberMax
}

// FillableChain is the chain a document may actually be approved by: numbers
// normalised, and every step nobody could fill left open.
//
// A step is unfillable in two ways. It may name something that is not a
// registration number, which no provider would vouch for. Or it may name a
// citizen an earlier step already names — one citizen signs a document once, so
// the later step would be owed to somebody who has already signed, and the
// document could never be approved by anybody.
//
// The steps must arrive in step_order: it decides which occurrence of a repeated
// citizen keeps the name. Both callers read them ordered, and the migration that
// repairs stored chains breaks the same tie the same way.
//
// Opening such a step is the only reading that leaves the chain completable: the
// tenant asked for that many approvals and still gets them, the step is simply
// fillable by whoever can actually sign. ValidateChain refuses to SAVE either
// shape; this is what the path that decides who may sign does with the chains
// stored before it did.
func FillableChain(steps []WorkflowStep) []WorkflowStep {
	out := make([]WorkflowStep, 0, len(steps))
	namedAt := map[string]bool{}
	for _, step := range steps {
		reg := NormaliseRegNumber(step.SignerRegNumber)
		if !PlausibleRegNumber(reg) || namedAt[reg] {
			reg = ""
		} else {
			namedAt[reg] = true
		}
		out = append(out, WorkflowStep{Order: step.Order, Name: step.Name, SignerRegNumber: reg})
	}
	return out
}

// ValidateChain is what a saved chain has to satisfy, and it answers with the
// chain as it should be stored.
//
// The document type is upper-cased and checked against the types this
// deployment knows, because a chain saved against a type nothing produces is a
// chain that never runs.
func ValidateChain(docType string, docTypes []string, steps []WorkflowStep) (string, []WorkflowStep, error) {
	docType = strings.ToUpper(strings.TrimSpace(docType))
	if !slices.Contains(docTypes, docType) {
		return "", nil, fmt.Errorf("%w: invalid doc_type %q", ErrInvalidConfiguration, docType)
	}
	if len(steps) > MaxChainSteps {
		return "", nil, fmt.Errorf("%w: an approval chain is limited to %d steps",
			ErrInvalidConfiguration, MaxChainSteps)
	}

	cleaned := make([]WorkflowStep, 0, len(steps))
	namedAt := map[string]int{}
	for i, step := range steps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			return "", nil, fmt.Errorf("%w: step %d needs a name", ErrInvalidConfiguration, i+1)
		}
		for field, value := range map[string]string{"name": name, "signer": step.SignerRegNumber} {
			if fault := TextFault(value); fault != "" {
				return "", nil, fmt.Errorf("%w: step %d's %s cannot be stored — %s",
					ErrInvalidConfiguration, i+1, field, fault)
			}
		}
		reg := NormaliseRegNumber(step.SignerRegNumber)
		// A step naming something no citizen could present is a step nobody can
		// fill: signing checks the identity a provider vouched for, and both
		// providers refuse a registration number under eight characters. Refused
		// here, so a chain is never stored in a shape the snapshot would have to
		// open — the screen would name a citizen the document's chain did not.
		if reg != "" && !PlausibleRegNumber(reg) {
			return "", nil, fmt.Errorf("%w: step %d names %q, which is %d characters — a registration number is %d to %d",
				ErrInvalidConfiguration, i+1, reg, utf8.RuneCountInString(reg), RegNumberLimit, RegNumberMax)
		}
		// One citizen signs a document once, so naming the same person at two
		// steps builds a chain that can never be completed — whatever the
		// signature policy says. This has to be refused when it is saved, not
		// discovered when a document sticks halfway.
		if reg != "" {
			if first, repeated := namedAt[reg]; repeated {
				return "", nil, fmt.Errorf("%w: steps %d and %d both name %s, and one citizen signs a document once",
					ErrInvalidConfiguration, first, i+1, reg)
			}
			namedAt[reg] = i + 1
		}
		cleaned = append(cleaned, WorkflowStep{Order: i + 1, Name: name, SignerRegNumber: reg})
	}
	return docType, cleaned, nil
}

// StepsCanRequireNamedSigners reports whether a chain could ever be completed
// under a policy that only accepts named signers: every step has to name one,
// and no two steps may name the same citizen, because one citizen signs a
// document once. A chain that fails this would leave the type unapprovable by
// anybody.
func StepsCanRequireNamedSigners(docType string, steps []WorkflowStep) error {
	if len(steps) == 0 {
		return fmt.Errorf("%w: the %s chain has no steps, so requiring a named signer would leave nobody able to sign", ErrInvalidConfiguration, docType)
	}

	seen := map[string]int{}
	for _, step := range steps {
		if step.SignerRegNumber == "" {
			return fmt.Errorf("%w: step %d of the %s chain (%s) names no signer, so requiring a named signer would leave it unfillable",
				ErrInvalidConfiguration, step.Order, docType, step.Name)
		}
		if first, repeated := seen[step.SignerRegNumber]; repeated {
			return fmt.Errorf("%w: steps %d and %d of the %s chain both name %s, and one citizen signs a document once",
				ErrInvalidConfiguration, first, step.Order, docType, step.SignerRegNumber)
		}
		seen[step.SignerRegNumber] = step.Order
	}
	return nil
}

// TextFault names what makes a string unstorable, or answers empty.
//
// Both faults are things PostgreSQL refuses at the wire rather than at the
// column, so without this the caller gets a driver error they cannot act on
// instead of a sentence about their own input.
func TextFault(value string) string {
	if strings.ContainsRune(value, 0) {
		return "it contains a NUL character"
	}
	if !utf8.ValidString(value) {
		return "it is not valid UTF-8"
	}
	return ""
}

// SignaturePolicy says how a document type may be signed. Every type has an
// effective policy: a type nobody has configured allows both national channels
// and names no signer, which is how the app behaved before the table existed.
type SignaturePolicy struct {
	DocType            string `json:"doc_type"`
	AllowEID           bool   `json:"allow_eid"`
	AllowDAN           bool   `json:"allow_dan"`
	RequireNamedSigner bool   `json:"require_named_signer"`
	Configured         bool   `json:"configured"`
}

// The two national signing channels.
const (
	SignerEID = "EID"
	SignerDAN = "DAN"
)

// DefaultSignaturePolicy is what a type falls back to while no row exists.
func DefaultSignaturePolicy(docType string) SignaturePolicy {
	return SignaturePolicy{DocType: docType, AllowEID: true, AllowDAN: true}
}

// Allows reports whether this policy admits a signing method. An unknown method
// is refused rather than allowed: the list is the two channels this platform
// has, and a third would be one nobody decided to accept.
func (p SignaturePolicy) Allows(method string) bool {
	switch method {
	case SignerEID:
		return p.AllowEID
	case SignerDAN:
		return p.AllowDAN
	default:
		return false
	}
}
