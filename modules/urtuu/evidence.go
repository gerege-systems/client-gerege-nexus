/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * Official documents behind a task.
 *
 * A ministry's order is a legal instrument, not a form field. When one exists,
 * the task carries a *reference* to it and nothing more: the document is filed
 * in the documents app of the organisation that raised the work, signed there
 * with eID by the people whose signatures it needs, kept under that
 * organisation's retention policy, and read there. What crosses the link is
 * that it exists, what it is called and whether it has been signed (§2.4).
 *
 * There is no Sign here and there is none in the contract either, deliberately:
 * a signature is a ceremony a person performs in front of their own eID app.
 * This module files the document and links to it; the signing happens in
 * Documents, where it always has.
 */

package urtuu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
	contract "github.com/gerege-systems/open-gerege-nexus/backend/pkg/urtuu"
)

// documentRequest is how a caller attaches paperwork to a task.
//
// Two ways, and both go through the same place. Naming an existing document
// links to one already filed — usually the order that was signed last week;
// giving a title files a new one, which is what somebody raising work does when
// the order is being written now.
type documentRequest struct {
	// DocumentID names something already filed here.
	DocumentID string `json:"document_id"`
	// Title files a new one. Ignored when DocumentID is set: attaching and
	// creating are different acts and doing both would leave a document nobody
	// asked for.
	Title string `json:"title"`
	// Type selects the organisation's signature and retention policy —
	// CONTRACT, REQUEST or APPROVAL. Empty takes the platform's default. How
	// many signatures that implies is the organisation's policy and not this
	// module's opinion.
	Type string `json:"type"`
}

func (d *documentRequest) empty() bool {
	return d == nil || (strings.TrimSpace(d.DocumentID) == "" && strings.TrimSpace(d.Title) == "")
}

// attachDocument resolves a request into one piece of evidence.
//
// It fails loudly rather than quietly dropping the attachment: a task sent
// downward saying "see the attached order" when no order was filed is worse
// than a task that was refused at the point of raising it.
func (m *Module) attachDocument(ctx context.Context, tenantID string, request *documentRequest) (contract.Evidence, error) {
	filer, err := nexus.Documents()
	if err != nil {
		// This build has no documents module. A distribution can be assembled
		// without one, so this is a configuration answer rather than a fault.
		return contract.Evidence{}, errors.New("this deployment has no document store, so a task cannot carry an official document")
	}

	var filed nexus.FiledDocument
	if id := strings.TrimSpace(request.DocumentID); id != "" {
		// Read back rather than trusted: this both proves the document exists
		// and proves it belongs to *this* organisation, because Document is
		// asked inside this tenant. An id from somewhere else answers not-found.
		filed, err = filer.Document(ctx, tenantID, id)
	} else {
		filed, err = filer.File(ctx, tenantID, nexus.DocumentDraft{
			Title: strings.TrimSpace(request.Title),
			Type:  strings.TrimSpace(request.Type),
		})
	}
	if err != nil {
		return contract.Evidence{}, err
	}

	return contract.Evidence{
		Kind: contract.EvidenceDocument,
		Ref:  filed.ID,
		// Whose id this is. Without it the far side has an identifier it could
		// look up in its own database and find a different document under.
		Installation:       m.link.InstallationID(),
		Title:              filed.Title,
		Signatures:         filed.SignatureCount,
		RequiredSignatures: filed.RequiredSignatures,
		Signed:             filed.Signed(),
	}, nil
}

// readEvidence decodes what is stored on a task. A malformed column is treated
// as none: evidence is a reference to something else, and a task whose
// attachment cannot be read is still a task.
func readEvidence(raw json.RawMessage) []contract.Evidence {
	if len(raw) == 0 {
		return nil
	}
	var list []contract.Evidence
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	return list
}

// withEvidence appends one reference to what a task already carries.
func withEvidence(raw json.RawMessage, added contract.Evidence) ([]byte, error) {
	list := readEvidence(raw)
	for _, existing := range list {
		// Attaching the same document twice is a double click, not a second
		// document.
		if existing.Ref == added.Ref && existing.Installation == added.Installation {
			return json.Marshal(list)
		}
	}
	return json.Marshal(append(list, added))
}

// refreshEvidence re-reads the signature state of documents filed *here*.
//
// A document is signed after the task was raised at least as often as before:
// the order goes out for signature while the work is already moving. The stored
// copy is what was communicated, and it is deliberately not rewritten by this —
// what is refreshed is the answer, so a screen on the filing side shows the
// current count and an update sent upward carries it.
//
// Documents filed elsewhere are left exactly as they arrived. They cannot be
// read from here, and a count invented for them would be a lie with a number in
// it.
func (m *Module) refreshEvidence(ctx context.Context, tenantID string, list []contract.Evidence) []contract.Evidence {
	if len(list) == 0 {
		return list
	}
	filer, err := nexus.Documents()
	if err != nil {
		return list
	}

	refreshed := make([]contract.Evidence, 0, len(list))
	for _, item := range list {
		if item.Kind != contract.EvidenceDocument || item.Installation != m.link.InstallationID() {
			refreshed = append(refreshed, item)
			continue
		}
		filed, err := filer.Document(ctx, tenantID, item.Ref)
		if err != nil {
			// Deleted, or the app was uninstalled. What was communicated stays
			// on the task — a reference that has stopped resolving is a fact
			// worth keeping, not one to erase.
			refreshed = append(refreshed, item)
			continue
		}
		item.Title = filed.Title
		item.Signatures = filed.SignatureCount
		item.RequiredSignatures = filed.RequiredSignatures
		item.Signed = filed.Signed()
		refreshed = append(refreshed, item)
	}
	return refreshed
}

// evidenceJSON renders a list for storage, never as SQL NULL: the column is
// `NOT NULL DEFAULT '[]'` and an empty list is the honest empty value.
func evidenceJSON(list []contract.Evidence) ([]byte, error) {
	if list == nil {
		list = []contract.Evidence{}
	}
	return json.Marshal(list)
}

// saveEvidence writes the refreshed references back.
//
// Called where an update is about to be reported upward, so that what the other
// installation is told is the state as of now rather than as of the moment the
// task was raised.
func (m *Module) saveEvidence(ctx context.Context, tenantID, taskID string, list []contract.Evidence) {
	encoded, err := evidenceJSON(list)
	if err != nil {
		return
	}
	_, _ = m.db.Exec(nexus.WithTenantID(ctx, tenantID),
		`UPDATE urtuu_tasks SET evidence = $2, updated_at = NOW() WHERE id = $1`, taskID, encoded)
}
