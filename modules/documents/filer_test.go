package documents

import (
	"testing"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// The adapter is where the contract could quietly stop being true: it copies
// field by field, and a field copied to the wrong place still compiles. The
// count and the requirement in particular decide whether a caller believes a
// document is signed.
func TestFiledCarriesTheProgressACallerRendersFrom(t *testing.T) {
	signedAt := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	got := filed(&Document{
		ID:                 "doc-1",
		TenantID:           "tenant-should-not-travel",
		Title:              "Шийдвэр",
		DocType:            "APPROVAL",
		Status:             "PENDING_APPROVAL",
		SignatureCount:     1,
		RequiredSignatures: 2,
		SignedAt:           &signedAt,
		SignatureHash:      "hash-should-not-travel",
		SignerRegNumber:    "AA90010111",
	})

	if got.ID != "doc-1" || got.Title != "Шийдвэр" || got.Type != "APPROVAL" {
		t.Fatalf("identity did not survive the copy: %+v", got)
	}
	if got.SignatureCount != 1 || got.RequiredSignatures != 2 {
		t.Fatalf("progress did not survive the copy: %+v", got)
	}
	if got.Signed() {
		t.Error("one signature of two is not signed")
	}

	got.SignatureCount = 2
	if !got.Signed() {
		t.Error("two signatures of two is signed")
	}
}

// A document that asks for no signatures is not a signed document. The
// distinction matters for a caller deciding whether to act on it: zero of zero
// reads as complete to any `count >= required` written by hand, which is why
// the helper exists rather than each caller comparing.
func TestADocumentThatNeedsNoSignaturesIsNotSigned(t *testing.T) {
	if (nexus.FiledDocument{SignatureCount: 0, RequiredSignatures: 0}).Signed() {
		t.Error("nothing was signed")
	}
}
