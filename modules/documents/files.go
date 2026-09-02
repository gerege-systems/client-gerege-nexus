/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * The file a document carries — what its signature is meant to cover.
 *
 * See docs/adr/0003-a-document-carries-what-is-signed.md. Until it existed this
 * app held only a title and a status, which is why its "signatures" were
 * approvals: there was nothing to sign.
 */

package documents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// maxAttachmentBody caps the multipart envelope, leaving room above the file
// itself for the encoding and the form's other fields. The same shape esign
// uses for the same reason.
const maxAttachmentBody = (domain.MaxArtifactBytes) + (9 << 20)

// AttachFile stores what a document is about.
//
// The file is checked before it is stored and the document is checked before
// the file is read: a signed document's attachment is frozen, and reading
// twenty-five megabytes to then refuse them wastes the caller's upload as well
// as this process's memory.
func (m *DocumentsModule) AttachFile(ctx context.Context, tenantID, docID, fileName, declaredType string,
	content []byte, actorUserID string) (domain.Artifact, error) {

	doc, err := m.getDocument(ctx, tenantID, docID)
	if err != nil {
		if isNoRows(err) {
			return domain.Artifact{}, ErrNotSignable
		}
		return domain.Artifact{}, fmt.Errorf("read document: %w", err)
	}
	if err := domain.CheckAttachable(doc.SignatureCount, content); err != nil {
		return domain.Artifact{}, err
	}

	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = doc.Title
	}
	if fault := textFault(fileName); fault != "" {
		return domain.Artifact{}, fmt.Errorf("%w: the file name cannot be stored — %s",
			ErrInvalidDocument, fault)
	}
	if len([]rune(fileName)) > TitleLimit {
		fileName = string([]rune(fileName)[:TitleLimit])
	}

	artifact := domain.Artifact{
		FileName: fileName,
		// From the bytes, not from what was claimed: a file that says PDF and
		// is not one would otherwise go down the PAdES rail and be refused by
		// the provider rather than here.
		ContentType: domain.SniffContentType(content, declaredType),
		SizeBytes:   int64(len(content)),
		SHA256:      domain.Digest(content),
	}

	// One row per document, and replacing it is only reachable while nothing
	// has been signed — which CheckAttachable has just established.
	if _, err := m.db.Exec(ctx, `
		INSERT INTO document_files
		    (document_id, tenant_id, file_name, content_type, size_bytes, sha256, content, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid)
		ON CONFLICT (document_id) DO UPDATE
		   SET file_name = EXCLUDED.file_name, content_type = EXCLUDED.content_type,
		       size_bytes = EXCLUDED.size_bytes, sha256 = EXCLUDED.sha256,
		       content = EXCLUDED.content, uploaded_by = EXCLUDED.uploaded_by,
		       created_at = NOW()`,
		docID, tenantID, artifact.FileName, artifact.ContentType,
		artifact.SizeBytes, artifact.SHA256, content, actorUserID); err != nil {
		return domain.Artifact{}, fmt.Errorf("store document file: %w", err)
	}

	nexus.Audit(ctx, tenantID, actorUserID, "documents.file_attached", docID, map[string]any{
		"file_name": artifact.FileName, "content_type": artifact.ContentType,
		"size_bytes": artifact.SizeBytes, "sha256": artifact.SHA256,
	})
	return artifact, nil
}

// ArtifactOf describes what a document carries, or the empty artifact when it
// carries nothing — which is not an error and is most documents.
func (m *DocumentsModule) ArtifactOf(ctx context.Context, tenantID, docID string) (domain.Artifact, error) {
	var artifact domain.Artifact
	err := m.db.QueryRow(ctx, `
		SELECT file_name, content_type, size_bytes, sha256
		  FROM document_files WHERE document_id = $1 AND tenant_id = $2`,
		docID, tenantID).Scan(&artifact.FileName, &artifact.ContentType,
		&artifact.SizeBytes, &artifact.SHA256)
	if isNoRows(err) {
		return domain.Artifact{}, nil
	}
	if err != nil {
		return domain.Artifact{}, fmt.Errorf("read document file: %w", err)
	}
	return artifact, nil
}

// FileOf returns the bytes with what describes them.
//
// The signed copy when the document has one: that is the artifact a person
// asked for, the one a verifier reads and the one worth keeping. The original
// is still there — SignedOnly reaches it — because it is what the ledger's
// covered digest names.
func (m *DocumentsModule) FileOf(ctx context.Context, tenantID, docID string) (domain.Artifact, []byte, error) {
	return m.fileOf(ctx, tenantID, docID, false)
}

// OriginalFileOf is the file as it was filed, whatever has been signed since.
// It is what the ledger's covered digest names, so it is what a check of a
// recorded signature has to be run against.
func (m *DocumentsModule) OriginalFileOf(ctx context.Context, tenantID, docID string) (domain.Artifact, []byte, error) {
	return m.fileOf(ctx, tenantID, docID, true)
}

func (m *DocumentsModule) fileOf(ctx context.Context, tenantID, docID string, originalOnly bool) (domain.Artifact, []byte, error) {
	var artifact domain.Artifact
	var content, signed []byte
	err := m.db.QueryRow(ctx, `
		SELECT file_name, content_type, size_bytes, sha256, content, signed_content
		  FROM document_files WHERE document_id = $1 AND tenant_id = $2`,
		docID, tenantID).Scan(&artifact.FileName, &artifact.ContentType,
		&artifact.SizeBytes, &artifact.SHA256, &content, &signed)
	if isNoRows(err) {
		return domain.Artifact{}, nil, ErrNoAttachment
	}
	if err != nil {
		return domain.Artifact{}, nil, fmt.Errorf("read document file: %w", err)
	}
	// What is stored is what was signed, and a store that has drifted from its
	// own digest must not be handed out as though it had not. The check is on
	// the ORIGINAL: the signed copy has bytes added to it by design, and its
	// digest is not the one recorded.
	if got := domain.Digest(content); got != artifact.SHA256 {
		return domain.Artifact{}, nil, fmt.Errorf("%w: stored %s, holds %s",
			ErrArtifactCorrupt, artifact.SHA256, got)
	}
	if !originalOnly && len(signed) > 0 {
		return artifact, signed, nil
	}
	return artifact, content, nil
}

// ErrNoAttachment and ErrArtifactCorrupt are the two ways a file is not there.
var (
	ErrNoAttachment = errors.New("this document carries no file")
	// ErrArtifactCorrupt is a stored file that no longer matches the digest
	// recorded with it. It is reported rather than repaired: something has
	// happened to a legal record and the operator has to know which document.
	ErrArtifactCorrupt = errors.New("the stored file does not match its recorded digest")
)

// --- HTTP -------------------------------------------------------------------

func (m *DocumentsModule) attachFileHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBody)
	file, header, err := r.FormFile("file")
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "a file is required")
		return
	}
	defer func() { _ = file.Close() }()

	// One byte past the ceiling, so that "too large" is decided here rather
	// than by whatever runs out of memory first.
	content, err := io.ReadAll(io.LimitReader(file, domain.MaxArtifactBytes+1))
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "the file could not be read")
		return
	}

	artifact, err := m.AttachFile(r.Context(), tenantID, chi.URLParam(r, "id"),
		nameOf(header), header.Header.Get("Content-Type"), content, actorID(r.Context()))
	if err != nil {
		writeAttachFailure(w, err)
		return
	}
	nexus.JSON(w, http.StatusOK, artifact)
}

func (m *DocumentsModule) downloadFileHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	artifact, content, err := m.FileOf(r.Context(), tenantID, chi.URLParam(r, "id"))
	if errors.Is(err, ErrNoAttachment) {
		nexus.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeWriteFailure(r.Context(), w, err, "failed to read the document's file")
		return
	}

	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", artifact.FileName))
	// A tenant's document leaving the platform: not cached, not sniffed.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// writeAttachFailure keeps the two refusals apart: a frozen document is a
// conflict with a state, and everything else is a request that was wrong.
func writeAttachFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrArtifactFrozen):
		nexus.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, domain.ErrArtifactEmpty), errors.Is(err, domain.ErrArtifactTooLarge),
		errors.Is(err, ErrInvalidDocument):
		nexus.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotSignable):
		nexus.Error(w, http.StatusNotFound, "document not found")
	default:
		nexus.Error(w, http.StatusInternalServerError, "could not store the file")
	}
}

// nameOf is the uploaded name with any path the client sent stripped: a
// filename is a label here, never a location.
func nameOf(header *multipart.FileHeader) string {
	name := header.Filename
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	return strings.TrimSpace(name)
}

// formatOrApproval is what a signature says it is when nobody set a format.
//
// The empty format belongs to the paths that predate ADR 0003 — the DAN
// ceremony and an eID approval on a document carrying nothing — and approval is
// what those are. Defaulting here rather than at every call site means a new
// path that forgets to say gets the weakest claim rather than the strongest.
func formatOrApproval(format domain.Format) domain.Format {
	if format == "" {
		return domain.FormatApproval
	}
	return format
}
