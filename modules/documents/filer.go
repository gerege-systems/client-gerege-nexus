/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"context"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// This module's half of nexus.DocumentFiler — the capability every other
// module reaches through, including the ones compiled from other repositories.
//
// It is an adapter rather than the module implementing the interface directly,
// for the reason the meeting booker is one: the module's own methods return
// *Document, which carries signature hashes, the signer's registry number and
// the whole ceremony history. Satisfying the interface with that type would
// make every field of it part of an ecosystem-wide contract, and none of those
// fields are a caller's business.

type filer struct{ m *DocumentsModule }

func (f filer) File(ctx context.Context, tenantID string, draft nexus.DocumentDraft) (nexus.FiledDocument, error) {
	doc, err := f.m.CreateDocument(ctx, tenantID, draft.Title, draft.Type)
	if err != nil {
		return nexus.FiledDocument{}, err
	}
	return filed(doc), nil
}

func (f filer) Document(ctx context.Context, tenantID, documentID string) (nexus.FiledDocument, error) {
	doc, err := f.m.getDocument(ctx, tenantID, documentID)
	if err != nil {
		return nexus.FiledDocument{}, err
	}
	return filed(doc), nil
}

// filed narrows the record to what the contract publishes.
func filed(doc *Document) nexus.FiledDocument {
	return nexus.FiledDocument{
		ID:                 doc.ID,
		Title:              doc.Title,
		Type:               doc.DocType,
		Status:             doc.Status,
		SignatureCount:     doc.SignatureCount,
		RequiredSignatures: doc.RequiredSignatures,
		SignedAt:           doc.SignedAt,
	}
}
