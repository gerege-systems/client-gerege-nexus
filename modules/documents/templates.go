package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrTemplateNotFound is returned for a template id this tenant does not hold.
var ErrTemplateNotFound = errors.New("template not found")

// ErrTemplateRetired is returned for a template that is there but has been retired.
// It is a different answer from "not found" because it is a different situation for
// the operator: the row is on their screen, greyed out, and telling them it does not
// exist sends them looking for something that is not wrong.
var ErrTemplateRetired = errors.New("this template has been retired, so it cannot start a document")

// ErrTemplateNameTaken is returned when a tenant already has a template under
// the requested name.
var ErrTemplateNameTaken = errors.New("a template with this name already exists")

// DocumentTemplate is a preset a clerk starts a document from. A document is a
// title and a type, so a template carries exactly those two things — there is no
// document body for it to fill in.
type DocumentTemplate struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	DocType      string    `json:"doc_type"`
	TitlePattern string    `json:"title_pattern"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

const templateColumns = `id, name, doc_type, title_pattern, active, created_at`

func scanTemplate(row rowScanner) (*DocumentTemplate, error) {
	var tpl DocumentTemplate
	if err := row.Scan(&tpl.ID, &tpl.Name, &tpl.DocType, &tpl.TitlePattern, &tpl.Active, &tpl.CreatedAt); err != nil {
		return nil, err
	}
	return &tpl, nil
}

// checkTemplateFields keeps a template inside what its columns hold. Both are
// VARCHAR(255); without this, pasting a longer one reaches Postgres and comes back
// as a 22001 the operator cannot read.
func checkTemplateFields(name, titlePattern string) error {
	if name == "" {
		return fmt.Errorf("%w: template name cannot be empty", ErrInvalidConfiguration)
	}
	for field, value := range map[string]string{"name": name, "title pattern": titlePattern} {
		if fault := textFault(value); fault != "" {
			return fmt.Errorf("%w: the %s cannot be stored — %s", ErrInvalidConfiguration, field, fault)
		}
	}
	if len([]rune(name)) > TitleLimit {
		return fmt.Errorf("%w: a template name is at most %d characters, this one is %d",
			ErrInvalidConfiguration, TitleLimit, len([]rune(name)))
	}
	if titlePattern == "" {
		return fmt.Errorf("%w: title pattern cannot be empty", ErrInvalidConfiguration)
	}
	if len([]rune(titlePattern)) > TitleLimit {
		return fmt.Errorf("%w: a title pattern is at most %d characters, this one is %d",
			ErrInvalidConfiguration, TitleLimit, len([]rune(titlePattern)))
	}
	return nil
}

// resolveTitlePattern fills the placeholders a template may carry. Anything it
// does not recognise is left alone, so an unknown token shows up in the created
// document instead of vanishing silently.
func resolveTitlePattern(pattern string, now time.Time) string {
	replacements := []string{
		"{year}", strconv.Itoa(now.Year()),
		"{month}", fmt.Sprintf("%02d", int(now.Month())),
		"{date}", now.In(nexus.Location()).Format("2006-01-02"),
	}
	return strings.NewReplacer(replacements...).Replace(pattern)
}

func (m *DocumentsModule) ListTemplates(ctx context.Context, tenantID string) ([]DocumentTemplate, error) {
	rows, err := m.db.Query(ctx,
		`SELECT `+templateColumns+` FROM document_templates
		  WHERE tenant_id = $1 ORDER BY active DESC, name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query templates: %w", err)
	}
	defer rows.Close()

	list := make([]DocumentTemplate, 0)
	for rows.Next() {
		tpl, err := scanTemplate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		list = append(list, *tpl)
	}
	return list, rows.Err()
}

func (m *DocumentsModule) CreateTemplate(ctx context.Context, tenantID, name, docType, titlePattern string) (*DocumentTemplate, error) {
	name = strings.TrimSpace(name)
	titlePattern = strings.TrimSpace(titlePattern)
	docType = strings.ToUpper(strings.TrimSpace(docType))

	if err := checkTemplateFields(name, titlePattern); err != nil {
		return nil, err
	}
	if docType == "" {
		docType = DocTypes[0]
	}
	if !slices.Contains(DocTypes, docType) {
		return nil, fmt.Errorf("%w: invalid doc_type %q", ErrInvalidConfiguration, docType)
	}

	tpl, err := scanTemplate(m.db.QueryRow(ctx,
		`INSERT INTO document_templates (tenant_id, name, doc_type, title_pattern)
		      VALUES ($1, $2, $3, $4)
		   RETURNING `+templateColumns,
		tenantID, name, docType, titlePattern))
	if isUniqueViolation(err) {
		return nil, ErrTemplateNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("insert template: %w", err)
	}

	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.template_created", tpl.ID, map[string]any{
		"name": tpl.Name, "doc_type": tpl.DocType,
	})

	return tpl, nil
}

// UpdateTemplate changes a template in place. A template that has produced
// documents cannot simply be replaced — the documents keep their titles — so
// editing one is about what it will produce next; `active` retires it from the list
// without deleting the record of what it was.
func (m *DocumentsModule) UpdateTemplate(ctx context.Context, tenantID, templateID, name, docType, titlePattern string, active bool) (*DocumentTemplate, error) {
	if uuid.Validate(templateID) != nil {
		return nil, ErrTemplateNotFound
	}

	name = strings.TrimSpace(name)
	titlePattern = strings.TrimSpace(titlePattern)
	docType = strings.ToUpper(strings.TrimSpace(docType))

	if err := checkTemplateFields(name, titlePattern); err != nil {
		return nil, err
	}
	if !slices.Contains(DocTypes, docType) {
		return nil, fmt.Errorf("%w: invalid doc_type %q", ErrInvalidConfiguration, docType)
	}

	tpl, err := scanTemplate(m.db.QueryRow(ctx,
		`UPDATE document_templates
		    SET name = $1, doc_type = $2, title_pattern = $3, active = $4
		  WHERE id = $5 AND tenant_id = $6
		  RETURNING `+templateColumns,
		name, docType, titlePattern, active, templateID, tenantID))
	if isUniqueViolation(err) {
		return nil, ErrTemplateNameTaken
	}
	if isNoRows(err) {
		return nil, ErrTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update template: %w", err)
	}

	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.template_changed", tpl.ID, map[string]any{
		"name": tpl.Name, "doc_type": tpl.DocType, "active": tpl.Active,
	})
	return tpl, nil
}

func (m *DocumentsModule) DeleteTemplate(ctx context.Context, tenantID, templateID string) error {
	if uuid.Validate(templateID) != nil {
		return ErrTemplateNotFound
	}
	// RETURNING, because after this the row is the only place the name existed and
	// the record is what a reader has left. "Somebody deleted template
	// 0f6f16cf-…" answers nothing.
	var name, docType string
	err := m.db.QueryRow(ctx,
		`DELETE FROM document_templates WHERE id = $1 AND tenant_id = $2
		 RETURNING name, doc_type`, templateID, tenantID).Scan(&name, &docType)
	if isNoRows(err) {
		return ErrTemplateNotFound
	}
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}

	nexus.Audit(ctx, tenantID, actorFor(ctx), "documents.template_deleted", templateID, map[string]any{
		"name": name, "doc_type": docType,
	})

	return nil
}

// CreateDocumentFromTemplate resolves the template's title pattern and routes the
// result exactly as a hand-typed document: the template decides what is created,
// never whether the approval rules apply.
func (m *DocumentsModule) CreateDocumentFromTemplate(ctx context.Context, tenantID, templateID string) (*Document, error) {
	if uuid.Validate(templateID) != nil {
		return nil, ErrTemplateNotFound
	}

	// Read without the active filter, so a retired template can be told apart from
	// one this tenant does not have.
	tpl, err := scanTemplate(m.db.QueryRow(ctx,
		`SELECT `+templateColumns+` FROM document_templates
		  WHERE id = $1 AND tenant_id = $2`, templateID, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load template: %w", err)
	}
	if !tpl.Active {
		return nil, ErrTemplateRetired
	}

	return m.CreateDocument(ctx, tenantID, resolveTitlePattern(tpl.TitlePattern, time.Now()), tpl.DocType)
}

func (m *DocumentsModule) listTemplatesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	list, err := m.ListTemplates(r.Context(), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "failed to fetch templates")
		return
	}
	nexus.JSON(w, http.StatusOK, list)
}

func (m *DocumentsModule) createTemplateHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		Name         string `json:"name"`
		DocType      string `json:"doc_type"`
		TitlePattern string `json:"title_pattern"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid template payload")
		return
	}

	tpl, err := m.CreateTemplate(r.Context(), tenantID, req.Name, req.DocType, req.TitlePattern)
	switch {
	case errors.Is(err, ErrTemplateNameTaken):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeWriteFailure(r.Context(), w, err, "failed to save template")
		return
	}
	nexus.JSON(w, http.StatusCreated, tpl)
}

func (m *DocumentsModule) updateTemplateHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		Name         string `json:"name"`
		DocType      string `json:"doc_type"`
		TitlePattern string `json:"title_pattern"`
		Active       bool   `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "invalid template payload")
		return
	}

	tpl, err := m.UpdateTemplate(r.Context(), tenantID, chi.URLParam(r, "id"),
		req.Name, req.DocType, req.TitlePattern, req.Active)
	switch {
	case errors.Is(err, ErrTemplateNotFound):
		nexus.Error(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, ErrTemplateNameTaken):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeWriteFailure(r.Context(), w, err, "failed to save template")
		return
	}
	nexus.JSON(w, http.StatusOK, tpl)
}

func (m *DocumentsModule) deleteTemplateHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	switch err := m.DeleteTemplate(r.Context(), tenantID, chi.URLParam(r, "id")); {
	case errors.Is(err, ErrTemplateNotFound):
		nexus.Error(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "failed to delete template")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *DocumentsModule) useTemplateHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireWorkspace(w, r)
	if !ok {
		return
	}

	doc, err := m.CreateDocumentFromTemplate(r.Context(), tenantID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrTemplateNotFound):
		nexus.Error(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, ErrTemplateRetired):
		// 409, not 404: the template is there. The screen hides Use on a retired row,
		// so reaching this means the page is older than the retirement — and the
		// answer has to say so, or the operator goes looking for a template they can
		// see in front of them.
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		// A pattern that resolves to an over-long title is the template's problem,
		// and the operator can only fix it if they are told which it is.
		writeWriteFailure(r.Context(), w, err, "failed to create document from template")
		return
	}
	nexus.JSON(w, http.StatusCreated, doc)
}
