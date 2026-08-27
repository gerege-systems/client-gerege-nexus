package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
)

// RetentionRule is how long a document type is kept. Nothing deletes on this
// schedule: the screen reports what is past its term and a person decides.
type RetentionRule struct {
	DocType     string     `json:"doc_type"`
	RetainYears int        `json:"retain_years"`
	Note        string     `json:"note"`
	Configured  bool       `json:"configured"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	// Expired counts the documents of this type already past the term, and Total
	// every document of this type — so a zero Expired can be told apart from a type
	// nothing has been filed under.
	//
	// Both are absent, rather than zero, when they could not be read: the save path
	// treats a failed count as non-fatal, and a screen shown "0 filed" for a type
	// with hundreds is worse than one shown nothing.
	Expired *int `json:"expired,omitempty"`
	Total   *int `json:"total,omitempty"`
}

// ListRetentionRules returns a row per document type: its rule if the tenant set
// one, and either way how many of its documents are past the term.
func (m *DocumentsModule) ListRetentionRules(ctx context.Context, tenantID string) ([]RetentionRule, error) {
	rules := map[string]RetentionRule{}

	rows, err := m.db.Query(ctx,
		`SELECT doc_type, retain_years, note, updated_at
		   FROM document_retention_rules WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query retention rules: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rule RetentionRule
		var updatedAt time.Time
		if err := rows.Scan(&rule.DocType, &rule.RetainYears, &rule.Note, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan retention rule: %w", err)
		}
		rule.Configured = true
		rule.UpdatedAt = &updatedAt
		rules[rule.DocType] = rule
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts, err := m.retentionCounts(ctx, tenantID, rules)
	if err != nil {
		return nil, err
	}

	list := make([]RetentionRule, 0, len(DocTypes))
	for _, docType := range DocTypes {
		rule, ok := rules[docType]
		if !ok {
			rule = RetentionRule{DocType: docType}
		}
		// The list always knows: a count failure here is fatal, so every row it
		// returns carries both numbers.
		count := counts[docType]
		expired, total := count.expired, count.total
		rule.Expired, rule.Total = &expired, &total
		list = append(list, rule)
	}
	return list, nil
}

type retentionCount struct{ total, expired int }

// retentionCounts asks the database how many documents of each type exist and how
// many are older than that type's term. The term is only known here, so it is
// passed in per type rather than joined: the rule table holds at most one row per
// type, and DocTypes is three entries long.
func (m *DocumentsModule) retentionCounts(ctx context.Context, tenantID string, rules map[string]RetentionRule) (map[string]retentionCount, error) {
	counts := map[string]retentionCount{}

	rows, err := m.db.Query(ctx,
		`SELECT doc_type, count(*) FROM document_records
		  WHERE tenant_id = $1 GROUP BY doc_type`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("count documents by type: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var docType string
		var total int
		if err := rows.Scan(&docType, &total); err != nil {
			return nil, fmt.Errorf("scan document count: %w", err)
		}
		counts[docType] = retentionCount{total: total}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for docType, rule := range rules {
		if rule.RetainYears <= 0 {
			continue
		}
		var expired int
		if err := m.db.QueryRow(ctx,
			`SELECT count(*) FROM document_records
			  WHERE tenant_id = $1 AND doc_type = $2
			    AND created_at < NOW() - make_interval(years => $3)`,
			tenantID, docType, rule.RetainYears).Scan(&expired); err != nil {
			return nil, fmt.Errorf("count expired %s documents: %w", docType, err)
		}
		entry := counts[docType]
		entry.expired = expired
		counts[docType] = entry
	}
	return counts, nil
}

// SaveRetentionRule upserts the rule for one document type.
func (m *DocumentsModule) SaveRetentionRule(ctx context.Context, tenantID, docType string, retainYears int, note string) (*RetentionRule, error) {
	if fault := textFault(note); fault != "" {
		return nil, fmt.Errorf("%w: the note cannot be stored — %s", ErrInvalidConfiguration, fault)
	}
	docType = strings.ToUpper(strings.TrimSpace(docType))
	if !slices.Contains(DocTypes, docType) {
		return nil, fmt.Errorf("%w: invalid doc_type %q", ErrInvalidConfiguration, docType)
	}
	if retainYears < 1 || retainYears > 100 {
		return nil, fmt.Errorf("%w: retain_years must be between 1 and 100, got %d", ErrInvalidConfiguration, retainYears)
	}

	saved := RetentionRule{DocType: docType, Configured: true}
	var updatedAt time.Time
	err := m.db.QueryRow(ctx,
		`INSERT INTO document_retention_rules (tenant_id, doc_type, retain_years, note, updated_at)
		      VALUES ($1, $2, $3, $4, NOW())
		 ON CONFLICT (tenant_id, doc_type) DO UPDATE
		    SET retain_years = EXCLUDED.retain_years,
		        note = EXCLUDED.note,
		        updated_at = NOW()
		 RETURNING retain_years, note, updated_at`,
		tenantID, docType, retainYears, strings.TrimSpace(note)).
		Scan(&saved.RetainYears, &saved.Note, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("save retention rule: %w", err)
	}

	saved.UpdatedAt = &updatedAt

	// The rule is stored by this point, so the change is recorded before anything
	// else can fail. Reporting a 500 for a rule that IS saved — and skipping its
	// audit entry — is worse than answering without the counts.
	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.retention_rule_changed", docType, map[string]any{
		"retain_years": saved.RetainYears,
	})

	// The response is the same shape the list returns, so it carries the same
	// counts rather than a pair of zeroes claiming nothing is filed. They are
	// presentation only: a failure here is logged and the saved rule still returned.
	counts, err := m.retentionCounts(ctx, tenantID, map[string]RetentionRule{docType: saved})
	if err != nil {
		// The rule is stored and recorded. The counts are left absent rather than
		// zero, so the screen keeps what it already knew instead of being told this
		// type has nothing filed under it.
		slog.WarnContext(ctx, "saved the retention rule but could not count its documents",
			"doc_type", docType, "error", err)
		return &saved, nil
	}
	count := counts[docType]
	expired, total := count.expired, count.total
	saved.Expired, saved.Total = &expired, &total

	return &saved, nil
}

func (m *DocumentsModule) listRetentionRulesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	list, err := m.ListRetentionRules(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "failed to fetch retention rules")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (m *DocumentsModule) saveRetentionRuleHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		RetainYears int    `json:"retain_years"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid retention rule payload")
		return
	}

	saved, err := m.SaveRetentionRule(r.Context(), tenantID, chi.URLParam(r, "docType"), req.RetainYears, req.Note)
	if err != nil {
		writeWriteFailure(r.Context(), w, err, "failed to save the retention rule")
		return
	}
	nexus.JSON(w, http.StatusOK, saved)
}
