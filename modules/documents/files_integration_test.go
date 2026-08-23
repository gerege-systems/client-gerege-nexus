package documents

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
)

// A document that carries a file is the difference between a signature over
// content and an approval of a title. These run against a real schema because
// the freeze rule, the digest and the row are all things the database holds.
func TestADocumentCarriesTheFileItsSignatureWillCover(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Nothing attached is not an error: it is what every document was until
	// this table existed, and most still are.
	if artifact, err := f.m.ArtifactOf(ctx, f.tenantID, doc.ID); err != nil || artifact.Present() {
		t.Fatalf("a bare document carries nothing: %+v %v", artifact, err)
	}
	if _, _, err := f.m.FileOf(ctx, f.tenantID, doc.ID); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("downloading nothing: %v", err)
	}

	content := []byte("%PDF-1.7\nБайгууллагын гэрээ")
	artifact, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "contract.pdf",
		"application/octet-stream", content, "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	// The bytes decide the type, whatever the client said.
	if artifact.ContentType != "application/pdf" {
		t.Fatalf("content type = %q, want application/pdf", artifact.ContentType)
	}
	if artifact.SHA256 != domain.Digest(content) || artifact.SizeBytes != int64(len(content)) {
		t.Fatalf("the artifact does not describe what was stored: %+v", artifact)
	}

	back, stored, err := f.m.FileOf(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatal("the bytes that came back are not the bytes that went in")
	}
	if back.SHA256 != artifact.SHA256 {
		t.Fatalf("digest drifted: %q vs %q", back.SHA256, artifact.SHA256)
	}

	// Replacing is allowed while nothing has been signed: a document is
	// prepared, then signed, and the order is the guarantee.
	if _, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "second.txt", "text/plain",
		[]byte("өөр агуулга"), ""); err != nil {
		t.Fatalf("replacing before any signature: %v", err)
	}
	replaced, _, err := f.m.FileOf(ctx, f.tenantID, doc.ID)
	if err != nil || replaced.FileName != "second.txt" || replaced.ContentType != "text/plain" {
		t.Fatalf("the replacement did not take: %+v %v", replaced, err)
	}

	// One document, one file.
	var rows int
	if err := f.m.db.QueryRow(ctx,
		`SELECT count(*) FROM document_files WHERE document_id = $1`, doc.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("a document carries one file, found %d", rows)
	}
}

// Once a signature names the bytes, the bytes stop moving.
func TestASignedDocumentsFileCannotBeReplaced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "contract.pdf", "application/pdf",
		[]byte("%PDF-1.7\nэх хувь"), ""); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = f.m.AttachFile(ctx, f.tenantID, doc.ID, "swapped.pdf", "application/pdf",
		[]byte("%PDF-1.7\nөөр текст"), "")
	if !errors.Is(err, domain.ErrArtifactFrozen) {
		t.Fatalf("a signed document took a new file: %v", err)
	}

	// And the original is still what it was. Read as the original on purpose:
	// a PAdES signature adds bytes to a copy of the document, and the digest
	// the ledger names is the one the original still has.
	back, stored, err := f.m.OriginalFileOf(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.Contains(string(stored), "эх хувь") {
		t.Fatalf("the stored file changed under the signature: %q", stored)
	}
	if back.SHA256 != domain.Digest(stored) {
		t.Fatal("the recorded digest no longer describes the stored bytes")
	}
}

// Another organisation's document is not there to read, whatever its id.
func TestAFileBelongsToTheOrganisationThatFiledIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "contract.pdf", "application/pdf",
		[]byte("%PDF-1.7\nнууц"), ""); err != nil {
		t.Fatalf("attach: %v", err)
	}

	stranger := "11111111-1111-1111-1111-111111111111"
	if artifact, err := f.m.ArtifactOf(ctx, stranger, doc.ID); err != nil || artifact.Present() {
		t.Fatalf("another organisation sees nothing: %+v %v", artifact, err)
	}
	if _, _, err := f.m.FileOf(ctx, stranger, doc.ID); !errors.Is(err, ErrNoAttachment) {
		t.Fatalf("another organisation downloads nothing: %v", err)
	}
}

// A document that carries a file is signed over that file, and the record says
// so. This is the whole point of ADR 0003: before it, the same ceremony
// recorded an approval of a title.
func TestAFiledDocumentIsSignedOverItsContent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A spreadsheet rather than a PDF: the PAdES rail signs documents it can
	// put a signature inside, and everything else is covered by its digest.
	content := []byte("огноо;дүн\n2026-08-20;100")
	artifact, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "amounts.csv", "text/csv", content, "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	signed, err := signWithEID(t, f, doc.ID, "AA90010111")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signed.Status != StatusApproved {
		t.Fatalf("status = %q, want %q", signed.Status, StatusApproved)
	}

	// The rail was asked to sign the file's digest and nothing else.
	f.signer.mu.Lock()
	asked := len(f.signer.asked)
	var sentDigest string
	for _, digest := range f.signer.asked {
		sentDigest = digest
	}
	f.signer.mu.Unlock()
	if asked != 1 || sentDigest != artifact.SHA256 {
		t.Fatalf("the rail was asked for %q, the file is %q (%d ceremonies)", sentDigest, artifact.SHA256, asked)
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
	// A signature over content, and the record says which content.
	if ledger[0].Format != domain.FormatDetached {
		t.Fatalf("format = %q, want %q", ledger[0].Format, domain.FormatDetached)
	}
	if ledger[0].Proof != domain.ProofSignature {
		t.Fatalf("proof = %q, want %q", ledger[0].Proof, domain.ProofSignature)
	}
	if ledger[0].CoveredDigest != artifact.SHA256 {
		t.Fatalf("covered = %q, want the file's digest %q", ledger[0].CoveredDigest, artifact.SHA256)
	}
}

// A rail that answers with a digest it was never given signed something else —
// a stale session, a mixed-up id — and recording it would put one document's
// signature on another.
func TestARailThatSignedSomethingElseIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "amounts.csv", "text/csv",
		[]byte("огноо;дүн\n2026-08-20;100"), ""); err != nil {
		t.Fatalf("attach: %v", err)
	}

	// The rail will confirm a different document's digest.
	f.signer.badDigest = domain.Digest([]byte("өөр баримт"))

	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); !errors.Is(err, ErrSignatureRejected) {
		t.Fatalf("a mismatched digest must be refused, got %v", err)
	}

	// And nothing was written: a refused ceremony leaves no signature behind.
	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != 0 {
		t.Fatalf("a refused ceremony left %d signatures", len(ledger))
	}
}

// A document carrying nothing keeps the ceremony it always had, and the record
// keeps saying what that is worth.
func TestADocumentWithNoFileIsStillApproved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Тэмдэглэл", "APPROVAL")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// The signing rail was never troubled: there was nothing to sign.
	f.signer.mu.Lock()
	asked := len(f.signer.asked)
	f.signer.mu.Unlock()
	if asked != 0 {
		t.Fatalf("the signing rail was asked %d times for a document with no file", asked)
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
	if ledger[0].Format != domain.FormatApproval || ledger[0].Proof != domain.ProofApproval {
		t.Fatalf("a document with nothing to sign records an approval: %+v", ledger[0])
	}
	if ledger[0].CoveredDigest != "" {
		t.Fatalf("an approval covers nothing, got %q", ledger[0].CoveredDigest)
	}
}

// A filed PDF is signed the strongest way this platform can: the signature goes
// inside the document, and what comes back verifies away from here.
func TestAFiledPDFIsSignedIntoTheDocumentItself(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	original := []byte("%PDF-1.7\nталуудын гэрээ")
	artifact, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "contract.pdf", "application/pdf", original, "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
	if ledger[0].Format != domain.FormatPAdES {
		t.Fatalf("format = %q, want %q", ledger[0].Format, domain.FormatPAdES)
	}
	if ledger[0].Proof != domain.ProofSignature {
		t.Fatalf("proof = %q, want a signature over content", ledger[0].Proof)
	}
	// What was covered is the original's digest: that is the file the record
	// names, and the signed copy is what came back from covering it.
	if ledger[0].CoveredDigest != artifact.SHA256 {
		t.Fatalf("covered = %q, want the original's digest %q", ledger[0].CoveredDigest, artifact.SHA256)
	}

	// Downloading gives the signed copy — that is the artifact a person wants.
	_, handed, err := f.m.FileOf(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Contains(handed, []byte("%%SIGNED")) {
		t.Fatal("the signed copy was not handed over")
	}
	// And the original is untouched, because it is what the ledger names.
	back, stored, err := f.m.OriginalFileOf(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("original: %v", err)
	}
	if !bytes.Equal(stored, original) || back.SHA256 != domain.Digest(original) {
		t.Fatal("the original changed under the signature")
	}
}

// A rail that signs digests but not documents still signs. Refusing the citizen
// over a format would refuse them a signature this installation can give.
func TestARailWithoutPDFSigningFallsBackToTheDigest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.signer.noPDF = true

	doc, err := f.m.CreateDocument(ctx, f.tenantID, "Гэрээ", "CONTRACT")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	content := []byte("%PDF-1.7\nталуудын гэрээ")
	artifact, err := f.m.AttachFile(ctx, f.tenantID, doc.ID, "contract.pdf", "application/pdf", content, "")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if _, err := signWithEID(t, f, doc.ID, "AA90010111"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	ledger, err := f.m.ListSignatures(ctx, f.tenantID, doc.ID)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
	if ledger[0].Format != domain.FormatDetached {
		t.Fatalf("format = %q, want the fallback %q", ledger[0].Format, domain.FormatDetached)
	}
	if ledger[0].CoveredDigest != artifact.SHA256 {
		t.Fatalf("covered = %q, want %q", ledger[0].CoveredDigest, artifact.SHA256)
	}
	// No signed copy was produced, because none was possible.
	_, handed, err := f.m.FileOf(ctx, f.tenantID, doc.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !bytes.Equal(handed, content) {
		t.Fatal("a digest ceremony must not leave a signed copy behind")
	}
}
