package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotRoutable is returned when a document cannot be sent for approval: it is
// missing, belongs to another tenant, or has already left DRAFT.
var ErrNotRoutable = errors.New("document not found or is not a draft")

// ErrAlreadySigned is returned when a citizen signs a document they have already
// signed. One approval per person is not progress through the workflow.
var ErrAlreadySigned = errors.New("this signer has already signed the document")

// The approval chain's shapes and its rules are backend/domain/documents. They
// are aliased rather than re-declared: the JSON here is the published one, and
// two structs would be two places for it to drift.
type (
	WorkflowStep     = domain.WorkflowStep
	DocumentWorkflow = domain.DocumentWorkflow
	ApprovalStep     = domain.ApprovalStep
)

// querier is satisfied by both the pool and a transaction, so a read that has to
// happen inside somebody else's transaction does not need its own copy.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// snapshotApprovalChain copies the type's chain onto the document and records how
// many signatures that comes to. A type with no chain leaves no steps and a
// requirement of one, which is how a document of such a type has always behaved.
//
// It must run inside the transaction that starts the document waiting, so a
// document can never exist in a state where its requirement and its steps
// disagree.
func (m *DocumentsModule) snapshotApprovalChain(ctx context.Context, tx pgx.Tx, tenantID, docID, docType string) error {
	// Read, then put through the same Go rule the save path enforces, then written.
	// The chain a document carries is what decides who may sign it, so it must never
	// be a chain ReplaceWorkflow would have refused — and the decision cannot be left
	// to SQL, whose idea of upper case depends on the cluster's ctype.
	stored, err := m.workflowStepsTx(ctx, tx, tenantID, docType)
	if err != nil {
		return fmt.Errorf("read approval chain to copy: %w", err)
	}
	for _, step := range domain.FillableChain(stored) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO document_approval_steps (tenant_id, document_id, step_order, name, signer_reg_number)
			      VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (document_id, step_order) DO NOTHING`,
			tenantID, docID, step.Order, step.Name, step.SignerRegNumber); err != nil {
			return fmt.Errorf("copy approval step %d onto document: %w", step.Order, err)
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE document_records
		    SET required_signatures = GREATEST(1, (
		          SELECT count(*) FROM document_approval_steps s WHERE s.document_id = $1))
		  WHERE id = $1 AND tenant_id = $2`, docID, tenantID); err != nil {
		return fmt.Errorf("pin approval requirement to document: %w", err)
	}
	return nil
}

// approvalPosition is where a document stands in its own chain: how many
// signatures it carries, which step comes next, and which citizens are held back
// for a step further along.
type approvalPosition struct {
	Applied int
	// Next is the step this signature would fill. Nil means the document carries
	// no chain and one open signature approves it.
	Next *ApprovalStep
	// Reserved are the registration numbers a LATER unfilled step names. They may
	// not fill an open step now: a citizen signs a document once, so spending their
	// signature early would leave their own step unfillable and the document
	// unapprovable.
	Reserved []string
}

// nextApprovalStep reads a document's position in its own chain: the next step is
// the LOWEST one no signature has filled.
//
// Counting signatures and adding one would be wrong. Signatures made before the
// chain became per-document were matched to the step that names their signer, not
// to their place in time, so a document can legitimately hold a signature on step
// two while step one is still open. Asking such a document for step two again would
// ask a citizen who has already signed, and stick for ever.
func (m *DocumentsModule) nextApprovalStep(ctx context.Context, q querier, tenantID, docID string) (*approvalPosition, error) {
	pos := &approvalPosition{}
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM document_signatures WHERE tenant_id = $1 AND document_id = $2`,
		tenantID, docID).Scan(&pos.Applied); err != nil {
		return nil, fmt.Errorf("count applied signatures: %w", err)
	}

	rows, err := q.Query(ctx,
		`SELECT st.step_order, st.name, st.signer_reg_number
		   FROM document_approval_steps st
		  WHERE st.tenant_id = $1 AND st.document_id = $2
		    AND NOT EXISTS (SELECT 1 FROM document_signatures s
		                     WHERE s.document_id = st.document_id
		                       AND s.step_order = st.step_order)
		  ORDER BY st.step_order`,
		tenantID, docID)
	if err != nil {
		return nil, fmt.Errorf("read unfilled approval steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var step ApprovalStep
		if err := rows.Scan(&step.Order, &step.Name, &step.SignerRegNumber); err != nil {
			return nil, fmt.Errorf("scan approval step: %w", err)
		}
		if pos.Next == nil {
			next := step
			pos.Next = &next
			continue
		}
		if step.SignerRegNumber != "" {
			pos.Reserved = append(pos.Reserved, step.SignerRegNumber)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pos, nil
}

// unfilledSteps counts the steps of a document's chain that no signature has
// filled. It is asked again after an insert, because "how many signatures" is not
// the same question as "is the chain complete" once a signature can land on a step
// other than the next one.
func (m *DocumentsModule) unfilledSteps(ctx context.Context, q querier, docID string) (int, error) {
	var left int
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM document_approval_steps st
		  WHERE st.document_id = $1
		    AND NOT EXISTS (SELECT 1 FROM document_signatures s
		                     WHERE s.document_id = st.document_id
		                       AND s.step_order = st.step_order)`, docID).Scan(&left); err != nil {
		return 0, fmt.Errorf("count unfilled approval steps: %w", err)
	}
	return left, nil
}

// DocumentSteps returns a document's own chain, so a screen can show who each
// remaining approval is waiting on.
func (m *DocumentsModule) DocumentSteps(ctx context.Context, tenantID, docID string) ([]ApprovalStep, error) {
	if uuid.Validate(docID) != nil {
		return nil, ErrNotSignable
	}
	return m.stepsForDocumentTx(ctx, m.db, tenantID, docID)
}

// stepsForDocumentTx reads a document's own chain through whatever querier it is
// given, so the transaction that wrote the chain can also read back what it wrote.
func (m *DocumentsModule) stepsForDocumentTx(ctx context.Context, q querier, tenantID, docID string) ([]ApprovalStep, error) {
	rows, err := q.Query(ctx,
		`SELECT step_order, name, signer_reg_number
		   FROM document_approval_steps
		  WHERE tenant_id = $1 AND document_id = $2 ORDER BY step_order`, tenantID, docID)
	if err != nil {
		return nil, fmt.Errorf("query document approval steps: %w", err)
	}
	defer rows.Close()

	list := make([]ApprovalStep, 0)
	for rows.Next() {
		var step ApprovalStep
		if err := rows.Scan(&step.Order, &step.Name, &step.SignerRegNumber); err != nil {
			return nil, fmt.Errorf("scan document approval step: %w", err)
		}
		list = append(list, step)
	}
	return list, rows.Err()
}

// AppliedSignature is one row of a document's signature ledger.
type AppliedSignature struct {
	SignerName      string `json:"signer_name"`
	SignerRegNumber string `json:"signer_reg_number"`
	SignerMethod    string `json:"signer_method"`
	// Proof is what this record establishes: an authenticated approval, or a
	// signature over the document's content. A reader that treats the two alike
	// is reading a legal record wrong. See ADR 0002 and ADR 0003.
	Proof domain.Proof `json:"proof"`
	// Format is how it covers the document — pades, detached or approval — and
	// CoveredDigest is what it covered. Empty on an approval: there was nothing
	// to cover.
	Format        domain.Format `json:"format"`
	CoveredDigest string        `json:"covered_digest,omitempty"`
	// SignatureHash is the ceremony this approval came from, not a hash of
	// anything. The name is what migration 00013 called the column and is kept
	// so that clients reading it keep working; `proof` is what says how much it
	// is worth.
	SignatureHash string    `json:"signature_hash"`
	SignedAt      time.Time `json:"signed_at"`
	// StepOrder is which approval of the document's chain this signature filled.
	StepOrder int `json:"step_order"`
	// The eID certificate the citizen approved with, when the provider returned
	// one. Empty for DAN, and for E-ID approvals whose certificate eID could not
	// parse — a signature is still valid without it, it just carries less proof.
	CertificateSerial string `json:"certificate_serial,omitempty"`
	CertificateIssuer string `json:"certificate_issuer,omitempty"`
}

// ListWorkflows returns the chain for every document type, including the types
// nobody has configured, so the screen shows the whole picture.
func (m *DocumentsModule) ListWorkflows(ctx context.Context, tenantID string) ([]DocumentWorkflow, error) {
	rows, err := m.db.Query(ctx,
		`SELECT doc_type, step_order, name, signer_reg_number
		   FROM document_workflow_steps
		  WHERE tenant_id = $1 ORDER BY doc_type, step_order`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query workflow steps: %w", err)
	}
	defer rows.Close()

	steps := map[string][]WorkflowStep{}
	for rows.Next() {
		var docType string
		var step WorkflowStep
		if err := rows.Scan(&docType, &step.Order, &step.Name, &step.SignerRegNumber); err != nil {
			return nil, fmt.Errorf("scan workflow step: %w", err)
		}
		steps[docType] = append(steps[docType], step)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	list := make([]DocumentWorkflow, 0, len(DocTypes))
	for _, docType := range DocTypes {
		chain := steps[docType]
		if chain == nil {
			chain = []WorkflowStep{}
		}
		list = append(list, DocumentWorkflow{DocType: docType, Steps: chain})
	}
	return list, nil
}

// maxChainSteps is the longest approval chain a tenant may configure. It is also the
// burst allowed on the signing routes, since a chain of that length is sometimes signed
// in one sitting.
const maxChainSteps = 10

// ReplaceWorkflow swaps the whole chain for one document type. Editing a chain
// step by step would let a half-applied edit decide who may approve, so the
// delete and the inserts share one transaction.
func (m *DocumentsModule) ReplaceWorkflow(ctx context.Context, tenantID, docType string, steps []WorkflowStep) (*DocumentWorkflow, error) {
	docType, cleaned, err := domain.ValidateChain(docType, DocTypes, steps)
	if err != nil {
		return nil, err
	}

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin workflow update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One lock per (tenant, type), taken by this screen and by the signature-policy
	// screen. It settles two races. Two admins saving the same chain: under READ
	// COMMITTED the second DELETE cannot see the rows the first inserted, so it
	// clears nothing and then either collides on step_order or silently discards the
	// caller's chain while reporting success. And a chain saved against a policy
	// read outside a transaction: this screen and the policy screen could each pass
	// their guard on a stale view of the other and commit the state both forbid.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`,
		tenantID, docType); err != nil {
		return nil, fmt.Errorf("lock approval chain: %w", err)
	}

	// The signature policy may insist that only named signers approve this type.
	// Saving a chain that could not satisfy it would lock the type out.
	var requireNamed bool
	if err := tx.QueryRow(ctx,
		`SELECT require_named_signer FROM document_signature_policies
		  WHERE tenant_id = $1 AND doc_type = $2`, tenantID, docType).Scan(&requireNamed); err != nil {
		if !isNoRows(err) {
			return nil, fmt.Errorf("read signature policy: %w", err)
		}
		requireNamed = false // an unconfigured type allows open steps
	}
	if requireNamed {
		if err := domain.StepsCanRequireNamedSigners(docType, cleaned); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM document_workflow_steps WHERE tenant_id = $1 AND doc_type = $2`,
		tenantID, docType); err != nil {
		return nil, fmt.Errorf("clear workflow steps: %w", err)
	}

	for _, step := range cleaned {
		if _, err := tx.Exec(ctx,
			`INSERT INTO document_workflow_steps (tenant_id, doc_type, step_order, name, signer_reg_number)
			      VALUES ($1, $2, $3, $4, $5)`,
			tenantID, docType, step.Order, step.Name, step.SignerRegNumber); err != nil {
			return nil, fmt.Errorf("insert workflow step %d: %w", step.Order, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workflow update: %w", err)
	}

	// Who must sign a document type is an authority decision, so a change to it is
	// recorded alongside the signatures it will govern — and the record carries the
	// chain itself, not just how long it is. "The CONTRACT chain went from three steps
	// to three steps" answers nothing about who was swapped in.
	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.approval_chain_changed", docType, map[string]any{
		"steps": len(cleaned), "chain": cleaned,
	})

	return &DocumentWorkflow{DocType: docType, Steps: cleaned}, nil
}

// workflowStepsTx reads one type's chain inside somebody else's transaction, so a
// guard and the write it protects see the same state.
func (m *DocumentsModule) workflowStepsTx(ctx context.Context, q querier, tenantID, docType string) ([]WorkflowStep, error) {
	rows, err := q.Query(ctx,
		`SELECT step_order, name, signer_reg_number
		   FROM document_workflow_steps
		  WHERE tenant_id = $1 AND doc_type = $2 ORDER BY step_order`, tenantID, docType)
	if err != nil {
		return nil, fmt.Errorf("query workflow steps: %w", err)
	}
	defer rows.Close()

	steps := make([]WorkflowStep, 0)
	for rows.Next() {
		var step WorkflowStep
		if err := rows.Scan(&step.Order, &step.Name, &step.SignerRegNumber); err != nil {
			return nil, fmt.Errorf("scan workflow step: %w", err)
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// RouteDocument sends a draft for approval. Nothing the app creates is a draft
// today — CreateDocument writes PENDING_APPROVAL — so this exists for rows that
// arrived any other way, and for the day drafting is added.
func (m *DocumentsModule) RouteDocument(ctx context.Context, tenantID, docID string) (*Document, error) {
	if uuid.Validate(docID) != nil {
		return nil, ErrNotRoutable
	}

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin route document: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var docType string
	err = tx.QueryRow(ctx,
		`UPDATE document_records SET status = $1
		  WHERE id = $2 AND tenant_id = $3 AND status = $4
		  RETURNING doc_type`,
		StatusPending, docID, tenantID, StatusDraft).Scan(&docType)
	if isNoRows(err) {
		return nil, ErrNotRoutable
	}
	if err != nil {
		return nil, fmt.Errorf("route document: %w", err)
	}

	// The document takes the chain as it stands at the moment it starts waiting.
	if err := m.snapshotApprovalChain(ctx, tx, tenantID, docID, docType); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit route document: %w", err)
	}

	// The chain the document was given is what it will be held to for the rest of its
	// life — the type's chain may be edited afterwards without reaching it — so the
	// record is where "which rules governed this document" is answerable from.
	//
	// Read after the commit, and best-effort. Nothing edits a document's own chain
	// once it exists, so this reads exactly what was written; and failing the routing
	// because a log line could not be filled in would be the wrong way round.
	given, err := m.stepsForDocumentTx(ctx, m.db, tenantID, docID)
	if err != nil {
		slog.WarnContext(ctx, "routed the document but could not read back its chain for the record",
			"document_id", docID, "error", err)
	}
	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.routed", docID, map[string]any{
		"doc_type": docType, "chain": given,
	})

	return m.getDocument(ctx, tenantID, docID)
}

// ListSignatures returns a document's signature ledger, oldest first.
func (m *DocumentsModule) ListSignatures(ctx context.Context, tenantID, docID string) ([]AppliedSignature, error) {
	if uuid.Validate(docID) != nil {
		return nil, ErrNotSignable
	}

	rows, err := m.db.Query(ctx,
		`SELECT signer_name, signer_reg_number, signer_method, signature_hash, signed_at,
		        COALESCE(step_order, 0),
		        COALESCE(certificate_serial, ''), COALESCE(certificate_issuer, ''),
		        COALESCE(format, ''), COALESCE(covered_digest, '')
		   FROM document_signatures
		  WHERE tenant_id = $1 AND document_id = $2
		  ORDER BY COALESCE(step_order, 0), signed_at`, tenantID, docID)
	if err != nil {
		return nil, fmt.Errorf("query signatures: %w", err)
	}
	defer rows.Close()

	list := make([]AppliedSignature, 0)
	for rows.Next() {
		var sig AppliedSignature
		if err := rows.Scan(&sig.SignerName, &sig.SignerRegNumber, &sig.SignerMethod,
			&sig.SignatureHash, &sig.SignedAt, &sig.StepOrder,
			&sig.CertificateSerial, &sig.CertificateIssuer,
			&sig.Format, &sig.CoveredDigest); err != nil {
			return nil, fmt.Errorf("scan signature: %w", err)
		}
		// A row written before ADR 0003 asked the question carries no format,
		// and approval is what it was — see the column's comment in migration
		// 00071. What it proves follows from the format rather than from the
		// rail, because the same rail now produces two different things.
		sig.Format = formatOrApproval(sig.Format)
		sig.Proof = sig.Format.Proof()
		list = append(list, sig)
	}
	return list, rows.Err()
}

func (m *DocumentsModule) listWorkflowsHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	list, err := m.ListWorkflows(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "failed to fetch approval chains")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (m *DocumentsModule) saveWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	// A POINTER, so an absent or null "steps" can be told from an empty one.
	//
	// They mean opposite things and one of them is destructive: an empty array is a
	// tenant saying "this type needs no chain", while a body that simply does not carry
	// the field is a client that has gone wrong. Both used to clear the chain — and
	// clearing it LOOSENS the type, because a type with no chain is approved by one
	// open signature, so a malformed request could quietly turn a three-approver
	// contract into a one-signature one. The policy endpoint next door already refuses
	// to make a type unsignable; this is the same rule pointing the other way.
	var req struct {
		Steps *[]WorkflowStep `json:"steps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid approval chain payload")
		return
	}
	if req.Steps == nil {
		nexus.Error(w, http.StatusBadRequest,
			"invalid approval chain payload: steps is required — send an empty array to clear the chain")
		return
	}

	saved, err := m.ReplaceWorkflow(r.Context(), tenantID, chi.URLParam(r, "docType"), *req.Steps)
	if err != nil {
		writeWriteFailure(r.Context(), w, err, "failed to save the approval chain")
		return
	}
	nexus.JSON(w, http.StatusOK, saved)
}

func (m *DocumentsModule) routeDocumentHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	doc, err := m.RouteDocument(r.Context(), tenantID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrNotRoutable):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "failed to route document")
		return
	}
	nexus.JSON(w, http.StatusOK, doc)
}

func (m *DocumentsModule) listDocumentStepsHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	list, err := m.DocumentSteps(r.Context(), tenantID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrNotSignable):
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "failed to fetch the approval chain")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (m *DocumentsModule) listSignaturesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	list, err := m.ListSignatures(r.Context(), tenantID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrNotSignable):
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "failed to fetch signatures")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}
