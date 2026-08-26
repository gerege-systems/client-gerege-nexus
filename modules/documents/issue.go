/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ТАРААЛТ: НЭГ ЗАГВАР, ХҮН БҮРД ТУСДАА ГЭРЭЭ.
//
// Зээлийн гэрээг 500 хүнтэй байгуулна гэдэг нь 500 хүн НЭГ гэрээний хамтрагч
// тал болно гэсэн үг БИШ: хүн бүртэй тус тусдаа хоёр талт гэрээ байгуулагдана.
// Зээлдэгч бусад зээлдэгчийг харах ёсгүй, нэг хүний татгалзал бусдын гэрээг
// хөндөх ёсгүй, хүн бүрийн гэрээ ӨӨРИЙНХӨӨ гарын үсгээр хүчин төгөлдөр болно.
//
// Тиймээс тараалт нь МАСТЕР гэрээнээс хүлээн авагч бүрд ХҮҮХЭД ГЭРЭЭ үүсгэнэ:
//
//	мастер   — гарчиг, дугаар, PDF (гаргагчийн PIN2-той нь), бичвэр, гаргагч тал
//	хүүхэд   — parent_document_id-гаар мастертаа холбогдсон БИЕ ДААСАН гэрээ:
//	           гаргагч талын хуулбар + ГАНЦ хүлээн авагч + түүний гарын үсэг
//	           зурагч. Төлөв, гарын үсэг, татгалзал — бүгд өөрийнх нь.
//
// Хүлээн авагчийн талд юу ч өөрчлөгдөхгүй: хүүхэд гэрээ нь ердийн хоёр талт
// гэрээ бөгөөд одоо байгаа бүх зам (ирсэн гэрээ, урилга, PIN2, тайлан) хэвээр
// үйлчилнэ.
//
// НЭГ гэрээнд ОЛОН тал гэдэг өөр хэрэгцээ хэвээрээ: худалдагч + худалдан
// авагч + гэрч. Түүнд талуудын хэсэг үйлчилнэ — тэнд хүн НЭГ БҮРЧЛЭН, юу хийж
// байгаагаа мэдэж нэмэгдэнэ.
package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
)

// issueRecipient нь тараалтын нэг хүлээн авагч — Excel-ийн мөр эсвэл
// дэлгэцээс гараар нэмсэн нэг бичлэг.
type issueRecipient struct {
	Name       string `json:"name"`
	OrgReg     string `json:"org_reg"`
	SignerName string `json:"signer_name"`
	SignerReg  string `json:"signer_reg"`
	Position   string `json:"position"`
	line       int
}

type issueSkip struct {
	Row    int    `json:"row,omitempty"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

type issuedChild struct {
	DocumentID string `json:"document_id"`
	Name       string `json:"name"`
}

// issueHandler нь POST /{id}/issue — JSON жагсаалт ЭСВЭЛ multipart файл.
func (m *DocumentsModule) issueHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	masterID := chi.URLParam(r, "id")

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, masterID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	}

	// Мастерын агуулга: PDF (гаргагч зурсан бол гарын үсэгтэй нь) эсвэл бичвэр.
	master, err := m.attachedMaster(r.Context(), tenantID, masterID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хавсралт уншигдсангүй")
		return
	}
	body, err := m.bodyOf(r.Context(), tenantID, masterID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "бичвэр уншигдсангүй")
		return
	}
	if master == nil && strings.TrimSpace(body) == "" {
		nexus.Error(w, http.StatusConflict,
			"тараахын өмнө PDF хавсаргах эсвэл гэрээний текстээ бичнэ үү")
		return
	}

	parties, err := m.PartiesOf(r.Context(), tenantID, masterID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд уншигдсангүй")
		return
	}
	issuer := issuerOf(parties)
	if issuer.ID == "" {
		nexus.Error(w, http.StatusConflict,
			"гэрээнд гаргагч талыг нэрлээгүй байна — «Гаргагч (бид)» үүрэгтэй тал нэмнэ үү")
		return
	}

	recipients, err := m.issueRecipients(r)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(recipients) == 0 {
		nexus.Error(w, http.StatusBadRequest, "нэг ч хүлээн авагч алга")
		return
	}
	if len(recipients) > importRowCap {
		nexus.Error(w, http.StatusBadRequest,
			fmt.Sprintf("%d хүлээн авагч байна — нэг тараалтад дээд тал нь %d", len(recipients), importRowCap))
		return
	}

	// Давхардлын хамгаалалт: энэ мастераас ИЖИЛ регистрт аль хэдийн
	// тараагдсан бол дахин үүсгэхгүй. Дахин дарсан админ 800 хуулбар биш,
	// «аль хэдийн явсан» гэсэн хариу авна.
	already, err := m.issuedRegs(r.Context(), tenantID, masterID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "өмнөх тараалт уншигдсангүй")
		return
	}

	issued := []issuedChild{}
	skips := []issueSkip{}
	for _, rec := range recipients {
		reg := strings.ToUpper(strings.TrimSpace(rec.SignerReg))
		switch {
		case strings.TrimSpace(rec.Name) == "":
			skips = append(skips, issueSkip{rec.line, rec.Name, "нэр алга"})
			continue
		case reg == "":
			skips = append(skips, issueSkip{rec.line, rec.Name, "гарын үсэг зурагчийн регистрийн дугаар алга — PIN2 хэнд ч очихгүй"})
			continue
		case already[reg]:
			skips = append(skips, issueSkip{rec.line, rec.Name, "энэ регистрт аль хэдийн тараагдсан"})
			continue
		}
		childID, err := m.issueOne(r.Context(), tenantID, masterID, shape, issuer, master, body, rec)
		if err != nil {
			skips = append(skips, issueSkip{rec.line, rec.Name, err.Error()})
			continue
		}
		already[reg] = true
		issued = append(issued, issuedChild{DocumentID: childID, Name: rec.Name})
	}

	// Мастер нь тараагдсанаа мэднэ: жагсаалтад «ноорог» гэж биш,
	// «N хүлээн авагчид явсан» гэж харагдана.
	if len(issued) > 0 {
		if _, err := m.db.Exec(r.Context(),
			`UPDATE document_records
			    SET contract_state = CASE WHEN contract_state IN ('NONE','DRAFT') THEN 'SENT' ELSE contract_state END,
			        sent_at = COALESCE(sent_at, NOW())
			  WHERE id = $1 AND tenant_id = $2`, masterID, tenantID); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "мастерын төлөв шинэчлэгдсэнгүй")
			return
		}
	}

	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.contract_issued", masterID,
		map[string]any{"issued": len(issued), "skipped": len(skips)})
	nexus.JSON(w, http.StatusOK, map[string]any{"issued": len(issued), "children": issued, "skipped": skips})
}

// issueRecipients нь хүсэлтээс жагсаалтыг уншина — Excel/CSV файл эсвэл JSON.
func (m *DocumentsModule) issueRecipients(r *http.Request) ([]issueRecipient, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/") {
		file, header, err := r.FormFile("file")
		if err != nil {
			return nil, fmt.Errorf("файл ирсэнгүй — multipart 'file' талбарт өгнө үү")
		}
		defer func() { _ = file.Close() }()
		rows, err := readRecipientRows(file, header.Filename)
		if err != nil {
			return nil, err
		}
		recipients := make([]issueRecipient, 0, len(rows))
		for _, row := range rows {
			recipients = append(recipients, issueRecipient{
				Name: row.name, OrgReg: row.orgReg, SignerName: row.signerName,
				SignerReg: row.signerReg, Position: row.position, line: row.line,
			})
		}
		return recipients, nil
	}

	var req struct {
		Recipients []issueRecipient `json:"recipients"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("хүсэлт уншигдсангүй")
	}
	for i := range req.Recipients {
		req.Recipients[i].line = i + 1
	}
	return req.Recipients, nil
}

// issuedRegs нь энэ мастераас аль хэдийн тараагдсан регистрийн дугаарууд.
func (m *DocumentsModule) issuedRegs(ctx context.Context, tenantID, masterID string) (map[string]bool, error) {
	rows, err := m.db.Query(ctx,
		`SELECT upper(g.reg_number)
		   FROM document_records c
		   JOIN document_parties p ON p.document_id = c.id AND p.party_role <> 'issuer'
		   JOIN document_party_signatories g ON g.party_id = p.id
		  WHERE c.parent_document_id = $1 AND c.tenant_id = $2`, masterID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	regs := map[string]bool{}
	for rows.Next() {
		var reg string
		if err := rows.Scan(&reg); err != nil {
			return nil, err
		}
		regs[reg] = true
	}
	return regs, rows.Err()
}

// issueOne нь нэг хүлээн авагчид нэг бие даасан гэрээ үүсгэнэ.
//
// Хүүхэд гэрээ бүр НЭГ гүйлгээ: бичлэг, хоёр тал, гарын үсэг зурагч, хөлдсөн
// хувь, үзэгчид, урилгын тэмдэглэл — хагас үүссэн гэрээ гэж байхгүй.
func (m *DocumentsModule) issueOne(ctx context.Context, tenantID, masterID string,
	shape contractShape, issuer Party, master *attachedFile, body string,
	rec issueRecipient) (string, error) {

	kind := KindOrganisation
	if strings.TrimSpace(rec.OrgReg) == "" {
		kind = KindPerson
	}
	signerName := strings.TrimSpace(rec.SignerName)
	if signerName == "" {
		if kind == KindPerson {
			// Хүн ӨӨРӨӨ зурна — «Захирал» биш, өөрийнх нь нэр.
			signerName = strings.TrimSpace(rec.Name)
		} else {
			signerName = "Захирал"
		}
	}

	// Хүлээн авагчийн хувийг ЭХЛЭЭД бэлдэнэ — гүйлгээнээс гадуур, учир нь
	// PDF зурах нь удаан бөгөөд алдвал сан хөндөгдөөгүй байх ёстой.
	recParty := Party{
		DisplayName:        strings.TrimSpace(rec.Name),
		RegistrationNumber: strings.TrimSpace(rec.OrgReg),
		Signatories:        []Signatory{{FullName: signerName}},
	}
	var text string
	var pdf []byte
	var sum, fname string
	if master != nil && master.Word != nil {
		var err error
		fname, text, pdf, sum, err = m.freezeFromWord(ctx, master, shape, issuer, recParty, body)
		if err != nil {
			return "", err
		}
	} else if master != nil {
		if strings.TrimSpace(body) != "" {
			text, _, _, _ = m.freezeTextFor(shape, issuer, recParty, body)
		}
		pdf, sum, fname = master.PDF, domain.Digest(master.PDF), master.FileName
	} else {
		substituted, fields, blocks, _ := m.freezeTextFor(shape, issuer, recParty, body)
		rendered, digest, err := Render(shape.Title, substituted, fields, blocks)
		if err != nil {
			return "", err
		}
		text, pdf, sum = substituted, rendered, digest
		fname = fileName(recParty.RegistrationNumber, "contract")
	}

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var childID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO document_records
		     (tenant_id, title, doc_type, status, contract_state, signing_mode,
		      contract_number, amount, currency, effective_from, effective_to, due_at,
		      parent_document_id, sent_at)
		 SELECT tenant_id, title, doc_type, status, 'SENT', 'counterpart',
		        contract_number, amount, currency, effective_from, effective_to, due_at,
		        id, NOW()
		   FROM document_records WHERE id = $1 AND tenant_id = $2
		 RETURNING id`, masterID, tenantID).Scan(&childID); err != nil {
		return "", fmt.Errorf("гэрээ үүссэнгүй: %w", err)
	}

	// Бичвэр хүүхдэдээ хамт явна: хүлээн авагч бичвэрээ уншина, мастер
	// хожим засагдсан ч энэ гэрээ хөдлөхгүй.
	if strings.TrimSpace(body) != "" {
		if _, err := tx.Exec(ctx,
			`INSERT INTO document_bodies (document_id, tenant_id, body)
			 VALUES ($1, $2, $3)`, childID, tenantID, body); err != nil {
			return "", fmt.Errorf("бичвэр хуулагдсангүй: %w", err)
		}
	}

	// Гаргагч тал — мастерынхаа хуулбар.
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_parties
		     (tenant_id, document_id, ordinal, party_role, party_kind, display_name,
		      legal_name, registration_number, address_line, contact_email, contact_phone, state)
		 VALUES ($1, $2, 1, 'issuer', $3, $4, $5, $6, $7, $8, $9, 'draft')`,
		tenantID, childID, issuer.Kind, issuer.DisplayName, issuer.LegalName,
		issuer.RegistrationNumber, issuer.AddressLine, issuer.ContactEmail,
		issuer.ContactPhone); err != nil {
		return "", fmt.Errorf("гаргагч тал үүссэнгүй: %w", err)
	}

	// Хүлээн авагч — ГАНЦААРАА, шууд илгээгдсэн төлөвтэй.
	var partyID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO document_parties
		     (tenant_id, document_id, ordinal, party_role, party_kind, display_name,
		      registration_number, state, invited_at)
		 VALUES ($1, $2, 2, 'counterparty', $3, $4, $5, 'invited', NOW())
		 RETURNING id`,
		tenantID, childID, kind, recParty.DisplayName, recParty.RegistrationNumber).
		Scan(&partyID); err != nil {
		return "", fmt.Errorf("хүлээн авагч тал үүссэнгүй: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_party_signatories
		     (tenant_id, party_id, full_name, position, reg_number)
		 VALUES ($1, $2, $3, $4, $5)`,
		tenantID, partyID, signerName, strings.TrimSpace(rec.Position),
		strings.ToUpper(strings.TrimSpace(rec.SignerReg))); err != nil {
		return "", fmt.Errorf("гарын үсэг зурагч үүссэнгүй: %w", err)
	}

	// Хөлдсөн хувь + үзэгчид + түүх — sendHandler-ийн яг ижил мөчүүд.
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_party_files
		     (party_id, tenant_id, counterparty_tenant_id, document_id,
		      file_name, size_bytes, sha256, content, body_text)
		 VALUES ($1, $2, NULL, $3, $4, $5, $6, $7, $8)`,
		partyID, tenantID, childID, fname, len(pdf), sum, pdf, text); err != nil {
		return "", fmt.Errorf("хөлдсөн хувь бичигдсэнгүй: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE document_parties SET audience = ARRAY[$2::uuid]
		  WHERE document_id = $1`, childID, tenantID); err != nil {
		return "", fmt.Errorf("үзэгчид бичигдсэнгүй: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, detail)
		 VALUES ($1, $2, $3, 'invited', 'тараалт')`, tenantID, partyID, childID); err != nil {
		return "", fmt.Errorf("түүх бичигдсэнгүй: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return childID, nil
}
