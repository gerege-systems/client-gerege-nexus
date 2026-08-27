package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// SignaturePolicy says how a document type may be signed. Every type has an
// effective policy: a type nobody has configured allows both national channels
// and names no signer, which is how the app behaved before the table existed.
//
// UpdatedAt is the only field the domain's policy does not carry: it is a fact
// about the row rather than about the rule.
type SignaturePolicy struct {
	DocType            string     `json:"doc_type"`
	AllowEID           bool       `json:"allow_eid"`
	AllowDAN           bool       `json:"allow_dan"`
	RequireNamedSigner bool       `json:"require_named_signer"`
	Configured         bool       `json:"configured"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
}

// defaultSignaturePolicy is the policy a type falls back to while no row exists.
func defaultSignaturePolicy(docType string) SignaturePolicy {
	rule := domain.DefaultSignaturePolicy(docType)
	return SignaturePolicy{
		DocType: rule.DocType, AllowEID: rule.AllowEID, AllowDAN: rule.AllowDAN,
		RequireNamedSigner: rule.RequireNamedSigner,
	}
}

// allows asks the domain, so that which channels a policy admits — and that an
// unknown one is refused rather than allowed — is decided in one place.
func (p SignaturePolicy) allows(method string) bool {
	return domain.SignaturePolicy{AllowEID: p.AllowEID, AllowDAN: p.AllowDAN}.Allows(method)
}

// SignaturePolicyFor reads the stored policy for a type, or the default when the
// tenant has not configured one.
func (m *DocumentsModule) SignaturePolicyFor(ctx context.Context, tenantID, docType string) (SignaturePolicy, error) {
	return m.signaturePolicyTx(ctx, m.db, tenantID, docType)
}

// signaturePolicyTx is the same read through whatever querier it is given, so the
// signature path can read the policy inside the transaction that holds the document's
// lock — the policy is part of who may sign, and that decision is made under the lock.
func (m *DocumentsModule) signaturePolicyTx(ctx context.Context, q querier, tenantID, docType string) (SignaturePolicy, error) {
	policy := defaultSignaturePolicy(docType)
	var updatedAt time.Time

	err := q.QueryRow(ctx,
		`SELECT allow_eid, allow_dan, require_named_signer, updated_at
		   FROM document_signature_policies
		  WHERE tenant_id = $1 AND doc_type = $2`, tenantID, docType).
		Scan(&policy.AllowEID, &policy.AllowDAN, &policy.RequireNamedSigner, &updatedAt)
	if err != nil {
		if isNoRows(err) {
			return policy, nil
		}
		return policy, fmt.Errorf("load signature policy: %w", err)
	}

	policy.Configured = true
	policy.UpdatedAt = &updatedAt
	return policy, nil
}

// ListSignaturePolicies returns the effective policy for every document type, so
// the screen shows what actually applies rather than only the rows that exist.
func (m *DocumentsModule) ListSignaturePolicies(ctx context.Context, tenantID string) ([]SignaturePolicy, error) {
	stored := map[string]SignaturePolicy{}

	rows, err := m.db.Query(ctx,
		`SELECT doc_type, allow_eid, allow_dan, require_named_signer, updated_at
		   FROM document_signature_policies WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query signature policies: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var policy SignaturePolicy
		var updatedAt time.Time
		if err := rows.Scan(&policy.DocType, &policy.AllowEID, &policy.AllowDAN,
			&policy.RequireNamedSigner, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan signature policy: %w", err)
		}
		policy.Configured = true
		policy.UpdatedAt = &updatedAt
		stored[policy.DocType] = policy
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	list := make([]SignaturePolicy, 0, len(DocTypes))
	for _, docType := range DocTypes {
		if policy, ok := stored[docType]; ok {
			list = append(list, policy)
			continue
		}
		list = append(list, defaultSignaturePolicy(docType))
	}
	return list, nil
}

// SaveSignaturePolicy upserts the policy for one document type.
func (m *DocumentsModule) SaveSignaturePolicy(ctx context.Context, tenantID string, policy SignaturePolicy) (*SignaturePolicy, error) {
	docType := strings.ToUpper(strings.TrimSpace(policy.DocType))
	if !slices.Contains(DocTypes, docType) {
		return nil, fmt.Errorf("%w: invalid doc_type %q", ErrInvalidConfiguration, docType)
	}
	if !policy.AllowEID && !policy.AllowDAN {
		return nil, fmt.Errorf("%w: a policy must allow at least one of E-ID or DAN, otherwise the type cannot be signed", ErrInvalidConfiguration)
	}

	// The policy and the chain are one setting in practice: requiring a named signer
	// means every step has to name one, and name a different one — a step left open
	// could never be filled, and two steps naming the same citizen could never both
	// be, because one citizen signs a document once. Either way the type would
	// become unapprovable by anybody.
	//
	// So the check and the write share a transaction holding the same lock the
	// chain screen takes. Read outside one, the two screens could each pass their
	// guard on a stale view of the other and commit exactly the state both forbid:
	// one saving a chain that names nobody while the other turns the requirement on.
	tx, err := m.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin signature policy update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`,
		tenantID, docType); err != nil {
		return nil, fmt.Errorf("lock approval chain: %w", err)
	}

	// What is stored now, read under the same lock. The guard below is about TURNING
	// the requirement on, so a policy that already has it on can still have its other
	// fields edited — otherwise a tenant who wanted to stop accepting DAN would be
	// refused over documents that were already stranded before they touched anything.
	var wasRequired bool
	if err := tx.QueryRow(ctx,
		`SELECT require_named_signer FROM document_signature_policies
		  WHERE tenant_id = $1 AND doc_type = $2`, tenantID, docType).Scan(&wasRequired); err != nil {
		if !isNoRows(err) {
			return nil, fmt.Errorf("read the stored signature policy: %w", err)
		}
		wasRequired = false // an unconfigured type does not require a named signer
	}

	if policy.RequireNamedSigner {
		steps, err := m.workflowStepsTx(ctx, tx, tenantID, docType)
		if err != nil {
			return nil, err
		}
		if err := domain.StepsCanRequireNamedSigners(docType, steps); err != nil {
			return nil, err
		}
		if !wasRequired {
			if err := m.pendingCanRequireNamedSigners(ctx, tx, tenantID, docType); err != nil {
				return nil, err
			}
		}
	}

	saved := SignaturePolicy{DocType: docType, Configured: true}
	var updatedAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO document_signature_policies
		        (tenant_id, doc_type, allow_eid, allow_dan, require_named_signer, updated_at)
		 VALUES ($1, $2, $3, $4, $5, NOW())
		 ON CONFLICT (tenant_id, doc_type) DO UPDATE
		    SET allow_eid = EXCLUDED.allow_eid,
		        allow_dan = EXCLUDED.allow_dan,
		        require_named_signer = EXCLUDED.require_named_signer,
		        updated_at = NOW()
		 RETURNING allow_eid, allow_dan, require_named_signer, updated_at`,
		tenantID, docType, policy.AllowEID, policy.AllowDAN, policy.RequireNamedSigner).
		Scan(&saved.AllowEID, &saved.AllowDAN, &saved.RequireNamedSigner, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("save signature policy: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit signature policy: %w", err)
	}

	saved.UpdatedAt = &updatedAt

	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.signature_policy_changed", docType, map[string]any{
		"allow_eid":            saved.AllowEID,
		"allow_dan":            saved.AllowDAN,
		"require_named_signer": saved.RequireNamedSigner,
	})

	return &saved, nil
}

// pendingCanRequireNamedSigners refuses to turn the requirement on while documents are
// already waiting that it would strand.
//
// A document is held to its OWN chain, copied when it started waiting, and a later
// configuration change never reaches it. So a type whose chain names somebody at every
// step can still have documents in the queue that name nobody: they were created before
// the chain existed, or under a version of it with an open step. Under the requirement
// no signature can land on those — no named signer to accept — and nothing else moves
// them either, because the module deletes nothing.
//
// The sister rule to stepsCanRequireNamedSigners, which asks the same question of the
// chain a FUTURE document would be given. Both refuse at the moment of the decision
// rather than letting it be discovered by a document that sticks.
func (m *DocumentsModule) pendingCanRequireNamedSigners(ctx context.Context, q querier, tenantID, docType string) error {
	var stranded int
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM document_records d
		  WHERE d.tenant_id = $1 AND d.doc_type = $2 AND d.status = $3
		    AND (
		          -- no chain of its own: it names nobody, so nobody could sign it
		          NOT EXISTS (SELECT 1 FROM document_approval_steps st
		                       WHERE st.document_id = d.id)
		          -- or a step still to be filled that names nobody
		          OR EXISTS (SELECT 1 FROM document_approval_steps st
		                      WHERE st.document_id = d.id
		                        AND st.signer_reg_number = ''
		                        AND NOT EXISTS (SELECT 1 FROM document_signatures s
		                                         WHERE s.document_id = st.document_id
		                                           AND s.step_order = st.step_order))
		        )`,
		tenantID, docType, StatusPending).Scan(&stranded); err != nil {
		return fmt.Errorf("count the pending documents a named-signer requirement would strand: %w", err)
	}
	if stranded > 0 {
		return fmt.Errorf("%w: %d %s document(s) are already waiting under a chain that names nobody for at least one remaining approval, and requiring a named signer would leave them signable by nobody — decide those first, then turn this on",
			ErrInvalidConfiguration, stranded, docType)
	}
	return nil
}

func (m *DocumentsModule) listSignaturePoliciesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	list, err := m.ListSignaturePolicies(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "failed to fetch signature policies")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (m *DocumentsModule) saveSignaturePolicyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		AllowEID           bool `json:"allow_eid"`
		AllowDAN           bool `json:"allow_dan"`
		RequireNamedSigner bool `json:"require_named_signer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid signature policy payload")
		return
	}

	saved, err := m.SaveSignaturePolicy(r.Context(), tenantID, SignaturePolicy{
		DocType:            chi.URLParam(r, "docType"),
		AllowEID:           req.AllowEID,
		AllowDAN:           req.AllowDAN,
		RequireNamedSigner: req.RequireNamedSigner,
	})
	if err != nil {
		writeWriteFailure(r.Context(), w, err, "failed to save the signature policy")
		return
	}
	nexus.JSON(w, http.StatusOK, saved)
}

// signingRailsHandler answers which channels may sign this document here.
//
// Two facts the screen had to guess at and could not: this organisation's
// policy for the type, and whether the installation has the channel at all. It
// offered both channels always, which on ДАН meant a button whose only outcome
// was a 503 — there is no live client for it on any deployment today.
func (m *DocumentsModule) signingRailsHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	doc, err := m.getDocument(r.Context(), tenantID, chi.URLParam(r, "id"))
	if err != nil {
		if isNoRows(err) {
			nexus.Error(w, http.StatusNotFound, "document not found")
			return
		}
		nexus.Error(w, http.StatusInternalServerError, "could not read the document")
		return
	}

	policy, err := m.SignaturePolicyFor(r.Context(), tenantID, doc.DocType)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "could not read the signature policy")
		return
	}

	nexus.JSON(w, http.StatusOK, map[string]any{
		"rails": domain.RailsFor(domain.SignaturePolicy{
			DocType:            policy.DocType,
			AllowEID:           policy.AllowEID,
			AllowDAN:           policy.AllowDAN,
			RequireNamedSigner: policy.RequireNamedSigner,
			Configured:         policy.Configured,
		}, m.eidSvc.Mode() != "unconfigured", m.danSvc.Mode() != "unconfigured"),
	})
}
