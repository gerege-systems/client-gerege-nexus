/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package documents

// Proof is what a signature record actually establishes.
//
// The distinction is not pedantry, it is the difference between two legal
// artifacts that this app had been recording under one word. See
// docs/adr/0002-one-signing-rail.md.
type Proof string

const (
	// ProofApproval — a citizen proved who they were and approved a prompt
	// naming this document, at a moment recorded here.
	//
	// It establishes identity, intent and time. It does NOT bind to the
	// document's content: nothing in the record covers the bytes, so a document
	// edited afterwards is not contradicted by anything stored. This is what
	// both national channels give this app, because a document_records row
	// holds no content to sign — the title, the type and the status, and that
	// is all.
	ProofApproval Proof = "approval"

	// ProofSignature — a signature over the document's content, verifiable
	// against it afterwards.
	//
	// Nothing in this app produces one today and the record must not claim it.
	// It is declared because the platform can: internal/platform/esign signs
	// PDFs it holds, through the rail nexus.Signer publishes. The day a
	// document here carries an artifact, this is what its record would say.
	ProofSignature Proof = "signature"

	// ProofUnknown is what an unrecognised method yields, so that a rail added
	// without deciding this question is visible rather than silently filed as
	// one of the two.
	ProofUnknown Proof = ""
)

// ProofOf reports what a signing method establishes on this platform.
//
// A table rather than a default, because the answer is a decision about a
// national channel and not something to be guessed from a string: adding a rail
// should mean answering this, and an unanswered one shows up as unknown instead
// of being quietly promoted.
func ProofOf(method string) Proof {
	switch method {
	case SignerEID, SignerDAN:
		// Both authenticate a citizen and record their approval. eID has no
		// separate document-signing endpoint on this path — the approval a
		// citizen gives with their own credentials is what is recorded — and
		// ДАН has no signing product on this platform at all.
		return ProofApproval
	default:
		return ProofUnknown
	}
}

// Rail is one signing channel as a screen needs to know about it: whether it
// may be used for this document, and if not, which of the two reasons applies.
type Rail struct {
	Method string `json:"method"`
	// Available is the only field a button should look at.
	Available bool `json:"available"`
	// Reason is empty when it is available, and otherwise says whose decision
	// this was. The two are different problems for different people: one is an
	// administrator's setting, the other is an operator's deployment.
	Reason RailReason `json:"reason,omitempty"`
}

// RailReason is why a channel cannot be used.
type RailReason string

const (
	// RailNotAllowed — this organisation's signature policy for this document
	// type does not permit the channel. An administrator can change it.
	RailNotAllowed RailReason = "not_allowed"
	// RailNotConfigured — the installation has no credentials for the channel,
	// so it would answer nothing whatever the policy said. Only an operator can
	// change it, and until they do a button for it is a button that fails.
	RailNotConfigured RailReason = "not_configured"
)

// RailsFor answers which channels may sign this document type here.
//
// Two facts, and both have to be true: the organisation allows the channel, and
// the installation has it. They were previously combined nowhere — the screen
// offered both channels always, the policy was enforced when the request
// arrived, and whether the deployment had the channel at all was discovered by
// the citizen not receiving anything. On ДАН that is every deployment: there is
// no live client for it, so the button could only ever fail.
//
// The policy is asked first because it is the answer that can be acted on: an
// administrator told "your organisation has switched this off" knows what to do,
// and one told "this deployment has no ДАН" does not.
func RailsFor(policy SignaturePolicy, eidConfigured, danConfigured bool) []Rail {
	configured := map[string]bool{SignerEID: eidConfigured, SignerDAN: danConfigured}
	rails := make([]Rail, 0, 2)
	for _, method := range []string{SignerEID, SignerDAN} {
		switch {
		case !policy.Allows(method):
			rails = append(rails, Rail{Method: method, Reason: RailNotAllowed})
		case !configured[method]:
			rails = append(rails, Rail{Method: method, Reason: RailNotConfigured})
		default:
			rails = append(rails, Rail{Method: method, Available: true})
		}
	}
	return rails
}
