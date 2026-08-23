package documents_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gerege-systems/client-gerege-nexus/domain/documents"
)

// What a document carries decides how it is signed — not the caller, and not
// the file's name.
func TestTheDocumentDecidesHowItIsSigned(t *testing.T) {
	pdf := documents.Artifact{ContentType: "application/pdf", SHA256: "abc"}
	other := documents.Artifact{ContentType: "image/png", SHA256: "abc"}
	none := documents.Artifact{}

	if got := documents.FormatFor(pdf, true); got != documents.FormatPAdES {
		t.Fatalf("a PDF on an installation that can sign PDFs: %q", got)
	}
	// No PDF rail: the digest still covers the bytes, and a detached signature
	// over a PDF is worth more than no signature.
	if got := documents.FormatFor(pdf, false); got != documents.FormatDetached {
		t.Fatalf("a PDF with no PAdES rail: %q", got)
	}
	if got := documents.FormatFor(other, true); got != documents.FormatDetached {
		t.Fatalf("anything else: %q", got)
	}
	// A document carrying nothing is the case every document was in until now,
	// and it is still a real one.
	if got := documents.FormatFor(none, true); got != documents.FormatApproval {
		t.Fatalf("no attachment: %q", got)
	}

	// And what each one is worth, which is what a reader of the record needs.
	if documents.FormatPAdES.Proof() != documents.ProofSignature ||
		documents.FormatDetached.Proof() != documents.ProofSignature {
		t.Fatal("both signing formats cover content and must say so")
	}
	if documents.FormatApproval.Proof() != documents.ProofApproval {
		t.Fatal("an approval covers no content")
	}
	if documents.Format("smartcard").Proof() != documents.ProofUnknown {
		t.Fatal("an undeclared format must not be promoted to either")
	}
}

// A file called contract.pdf that does not begin %PDF- is not a PDF, and
// sending it down the PAdES rail produces an error from the provider rather
// than an answer here.
func TestWhatAFileIsComesFromItsBytes(t *testing.T) {
	realPDF := []byte("%PDF-1.7\nbody")
	if got := documents.SniffContentType(realPDF, "application/octet-stream"); got != "application/pdf" {
		t.Fatalf("PDF bytes are a PDF whatever was declared: %q", got)
	}
	// The dangerous direction: a claim of PDF over bytes that are not one.
	if got := documents.SniffContentType([]byte("PK\x03\x04zip"), "application/pdf"); got != "application/octet-stream" {
		t.Fatalf("a false PDF claim must not reach the PAdES rail: %q", got)
	}
	// An honest declaration for a type this app does not recognise is kept.
	if got := documents.SniffContentType([]byte("\x89PNG"), "image/png"); got != "image/png" {
		t.Fatalf("a declared type this app cannot check is carried as given: %q", got)
	}
	if got := documents.SniffContentType([]byte("hello"), ""); got != "application/octet-stream" {
		t.Fatalf("nothing declared and nothing recognised: %q", got)
	}
	// Short files must not panic the sniffer.
	if got := documents.SniffContentType([]byte("%PD"), ""); got != "application/octet-stream" {
		t.Fatalf("a file shorter than the marker: %q", got)
	}
}

// A signature names bytes. Bytes that can be swapped afterwards make the
// signature a statement about nothing.
func TestASignedDocumentsFileIsFrozen(t *testing.T) {
	content := []byte("a contract")

	if err := documents.CheckAttachable(0, content); err != nil {
		t.Fatalf("an unsigned document takes a file: %v", err)
	}
	if err := documents.CheckAttachable(1, content); !errors.Is(err, documents.ErrArtifactFrozen) {
		t.Fatalf("a signed document must refuse a new file: %v", err)
	}

	if err := documents.CheckAttachable(0, nil); !errors.Is(err, documents.ErrArtifactEmpty) {
		t.Fatalf("an empty upload: %v", err)
	}
	oversize := make([]byte, documents.MaxArtifactBytes+1)
	if err := documents.CheckAttachable(0, oversize); !errors.Is(err, documents.ErrArtifactTooLarge) {
		t.Fatalf("past the ceiling: %v", err)
	}
	// Exactly at the ceiling is allowed: a limit somebody is told about should
	// be the limit they can reach.
	if err := documents.CheckAttachable(0, make([]byte, documents.MaxArtifactBytes)); err != nil {
		t.Fatalf("at the ceiling: %v", err)
	}
	// The freeze is checked before the size, because "you cannot replace this"
	// is the answer that is true whatever they uploaded.
	if err := documents.CheckAttachable(1, oversize); !errors.Is(err, documents.ErrArtifactFrozen) {
		t.Fatalf("a signed document, whatever the file: %v", err)
	}
}

// One shape for the digest everywhere, because three parts of this app compare
// it: the store, the signature, and whoever checks a file is the one signed.
func TestTheDigestIsOneShape(t *testing.T) {
	// The SHA-256 of "abc", which is a value anybody can check against another
	// implementation rather than against this one.
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := documents.Digest([]byte("abc")); got != abc {
		t.Fatalf("digest = %q, want %q", got, abc)
	}
	if got := documents.Digest([]byte("abc")); strings.ToLower(got) != got {
		t.Fatal("the digest is compared as a string and must be lowercase")
	}
	if documents.Digest([]byte("abc")) == documents.Digest([]byte("abd")) {
		t.Fatal("different bytes, different digest")
	}

	artifact := documents.Artifact{SHA256: documents.Digest([]byte("abc"))}
	if !artifact.Present() {
		t.Fatal("an artifact with a digest is present")
	}
	if (documents.Artifact{}).Present() {
		t.Fatal("a document carrying nothing is not carrying something")
	}
}
