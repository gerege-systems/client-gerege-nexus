/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// Гэрээний БИЧВЭР ба түүнийг талууд руу ИЛГЭЭХ.
//
// Энэ хоёр нь тус тусдаа файлд байх боломжтой ч нэг дор байх нь зөв: илгээх
// гэдэг нь бичвэрийг тал бүрийн нэрээр зурж, байтыг нь ХӨЛДӨӨХ үйлдэл юм.
// Хоёрыг салгавал хөлдөх агшин хаана байгаа нь кодоос харагдахаа болино.
//
// # ХӨЛДСӨН БАЙТ НЬ ЭНЭ ФАЙЛЫН ГОЛ САНАА
//
// Тал бүрд хүргэсэн агшинд PDF зурагдаж, `document_party_files`-д
// хадгалагдана. Дахин ХЭЗЭЭ Ч үүсгэгдэхгүй: fpdf-ийн `/CreationDate`
// ганцаараа байтыг хөдөлгөх ба хөдөлмөгц гарын үсгийн `covered_digest` нь
// оршихгүй байтыг нэрлэнэ. «Би юунд гарын үсэг зурав» гэдэг асуулт
// хариултгүй болно.
package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// maxBodyChars нь гэрээний бичвэрийн дээд урт.
//
// Тэмдэгтээр, байтаар биш: кирилл үсэг UTF-8-д хоёр байт эзэлдэг тул байтаар
// хэмжвэл монгол гэрээ англиасаа хоёр дахин богино болно.
const maxBodyChars = 120_000

// ─────────────────────────────────────────────────────────────── бичвэр

func (m *DocumentsModule) getBodyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")
	var body string
	var updatedAt *time.Time
	err := m.db.QueryRow(r.Context(),
		`SELECT b.body, b.updated_at FROM document_bodies b
		  JOIN document_records d ON d.id = b.document_id AND d.tenant_id = b.tenant_id
		 WHERE b.document_id = $1 AND b.tenant_id = $2`, docID, tenantID).Scan(&body, &updatedAt)
	if isNoRows(err) {
		// Бичвэргүй гэрээ нь алдаа биш — хараахан бичээгүй гэсэн үг.
		nexus.JSON(w, http.StatusOK, map[string]any{"body": ""})
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "бичвэр уншигдсангүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"body": body, "updated_at": updatedAt})
}

func (m *DocumentsModule) saveBodyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")

	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "хүсэлт уншигдсангүй")
		return
	}
	if len([]rune(req.Body)) > maxBodyChars {
		nexus.Error(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("гэрээний бичвэр %d тэмдэгтээс урт байна", maxBodyChars))
		return
	}

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	}
	// Илгээгдсэн гэрээний бичвэр өөрчлөгдөхгүй. Хөлдсөн байт нь тал бүрийн
	// гарт байгаа бөгөөд эх бичвэрийг засах нь тэдгээртэй зөрөх ба хэн юунд
	// гарын үсэг зурсныг эргэлзээтэй болгоно.
	if shape.State != ContractDraft && shape.State != ContractNone {
		nexus.Error(w, http.StatusConflict, ErrNotDraft.Error())
		return
	}
	if shape.Signed > 0 {
		nexus.Error(w, http.StatusConflict, "гарын үсэг зурагдсан баримтын бичвэр өөрчлөгдөхгүй")
		return
	}

	if _, err := m.db.Exec(r.Context(),
		`INSERT INTO document_bodies (document_id, tenant_id, body, updated_by)
		 VALUES ($1, $2, $3, NULLIF($4,'')::uuid)
		 ON CONFLICT (document_id) DO UPDATE
		   SET body = EXCLUDED.body, updated_at = NOW(), updated_by = EXCLUDED.updated_by`,
		docID, tenantID, req.Body, actorFor(r.Context())); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "бичвэр хадгалагдсангүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.body_saved", docID, nil)
	nexus.JSON(w, http.StatusOK, map[string]any{"body": req.Body})
}

// ─────────────────────────────────────────────────────────────── илгээх

type sendSkip struct {
	PartyID string `json:"party_id"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// sendHandler нь гэрээг талууд руу хүргэнэ.
//
// Хүргэх агшинд тал бүрийн PDF зурагдаж хөлдөнө. Гарын үсэг зурах эрх бүхий
// хүнгүй талыг АЛГАСАЖ, НЭРЛЭЖ хэлнэ: чимээгүй алгасах нь гаргагчид «бүгдэд
// хүргэгдсэн» гэж уншигдана.
func (m *DocumentsModule) sendHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")

	var req struct {
		Mode     string   `json:"mode"`
		PartyIDs []string `json:"party_ids"`
		DueAt    *string  `json:"due_at"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Mode == "" {
		req.Mode = ModeCounterpart
	}
	if req.Mode != ModeCounterpart && req.Mode != ModeJoint {
		nexus.Error(w, http.StatusBadRequest, "горим нь counterpart эсвэл joint байна")
		return
	}

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	}
	if shape.State == ContractExecuted || shape.State == ContractWithdrawn {
		nexus.Error(w, http.StatusConflict, "энэ гэрээ хаагдсан байна")
		return
	}

	body, err := m.bodyOf(r.Context(), tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "бичвэр уншигдсангүй")
		return
	}
	if strings.TrimSpace(body) == "" {
		nexus.Error(w, http.StatusConflict, "гэрээний бичвэр хоосон байна")
		return
	}

	parties, err := m.PartiesOf(r.Context(), tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд уншигдсангүй")
		return
	}
	if len(parties) == 0 {
		nexus.Error(w, http.StatusConflict, "гэрээнд тал нэрлээгүй байна")
		return
	}

	only := map[string]bool{}
	for _, id := range req.PartyIDs {
		only[id] = true
	}

	// ҮЗЭГЧДИЙГ эхлээд бичнэ.
	//
	// `audience` бол гэрээний ХАМТЫН нэр төрх: гэрээнд оролцож буй бүх
	// байгууллага. `parties_see_each_other` бодлого ЗӨВХӨН үүнээс уншдаг
	// тул хоосон массив нь хүлээн авагчид өөрөөс нь бусад талыг —
	// гаргагчийг ч — үл харагдуулна: хэнтэй гэрээ байгуулж байгаагаа
	// мэдэхгүй хүн юунд нэгдэж байгаагаа мэдэхгүй.
	//
	// Хуулбар бичихээс ӨМНӨ: хүлээн авагч эхний секундээс бүрэн зургийг
	// хардаг байх ёстой.
	if _, err := m.db.Exec(r.Context(),
		`UPDATE document_parties p
		    SET audience = a.ids, updated_at = NOW()
		   FROM (SELECT array_agg(DISTINCT t) AS ids FROM (
		           SELECT tenant_id AS t FROM document_parties WHERE document_id = $1
		           UNION
		           SELECT counterparty_tenant_id FROM document_parties
		            WHERE document_id = $1 AND counterparty_tenant_id IS NOT NULL) u) a
		  WHERE p.document_id = $1 AND p.tenant_id = $2
		    AND p.audience IS DISTINCT FROM a.ids`, docID, tenantID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гэрээний үзэгчид бичигдсэнгүй")
		return
	}

	issuer := issuerOf(parties)
	sent := 0
	skips := []sendSkip{}
	for _, p := range parties {
		if len(only) > 0 && !only[p.ID] {
			continue
		}
		// Гаргагч өөрөө хүлээн авагч биш: түүний хувь нь мастер.
		if p.Role == RoleIssuer {
			continue
		}
		if p.State == PartySigned || p.State == PartyDeclined {
			skips = append(skips, sendSkip{p.ID, p.DisplayName, "аль хэдийн шийдвэрээ өгсөн"})
			continue
		}
		// Өөр байгууллагын тал өөрсдөө гарын үсэг зурагчаа нэрлэнэ — тэднийг
		// эрх бүхий хүнгүй гэж алгасахгүй.
		if len(p.Signatories) == 0 && p.CounterpartyTenant == nil {
			skips = append(skips, sendSkip{p.ID, p.DisplayName, ErrNoSignatory.Error()})
			continue
		}

		text, pdf, sum, err := m.freezeFor(r.Context(), tenantID, docID, shape, issuer, p, body)
		if err != nil {
			skips = append(skips, sendSkip{p.ID, p.DisplayName, err.Error()})
			continue
		}
		if err := m.storePartyCopy(r.Context(), tenantID, docID, p, text, pdf, sum); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "хүргэлт хадгалагдсангүй")
			return
		}
		sent++
	}

	if sent > 0 {
		if _, err := m.db.Exec(r.Context(),
			`UPDATE document_records
			    SET signing_mode = $3, contract_state = $4, sent_at = COALESCE(sent_at, NOW())
			  WHERE id = $1 AND tenant_id = $2`,
			docID, tenantID, req.Mode, ContractSent); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "гэрээний төлөв шинэчлэгдсэнгүй")
			return
		}
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.contract_sent", docID,
		map[string]any{"sent": sent, "skipped": len(skips), "mode": req.Mode})
	nexus.JSON(w, http.StatusOK, map[string]any{"sent": sent, "skipped": skips})
}

func issuerOf(parties []Party) Party {
	for _, p := range parties {
		if p.Role == RoleIssuer {
			return p
		}
	}
	return Party{}
}

func (m *DocumentsModule) bodyOf(ctx context.Context, tenantID, docID string) (string, error) {
	var body string
	err := m.db.QueryRow(ctx,
		`SELECT body FROM document_bodies WHERE document_id = $1 AND tenant_id = $2`,
		docID, tenantID).Scan(&body)
	if isNoRows(err) {
		return "", nil
	}
	return body, err
}

// freezeFor нь тал бүрийн бичвэрийг орлуулж, PDF-ийг нэг л удаа зурна.
//
// Бичвэрийг НЭГ удаа орлуулж PDF ба хүснэгт хоёуланд өгнө — хоёр тусдаа
// орлуулалт бол зөрөх боломжтой хоёр үнэн.
func (m *DocumentsModule) freezeFor(ctx context.Context, tenantID, docID string,
	shape contractShape, issuer, p Party, body string) (string, []byte, string, error) {

	f := Fields{
		SchoolName:   p.DisplayName,
		SchoolCode:   p.RegistrationNumber,
		Aimag:        p.AddressLine,
		Principal:    signatoryName(p),
		ContractCode: shape.DocType,
		Title:        shape.Title,
		Date:         time.Now(),
	}
	text := Substitute(body, f)

	var blocks []SignatureBlock
	if issuer.ID != "" {
		blocks = append(blocks, SignatureBlock{
			Label: "Захиалагч", Name: firstNonEmpty(issuer.LegalName, issuer.DisplayName),
			Etsi: issuer.RegistrationNumber,
		})
	}
	pdf, sum, err := Render(shape.Title, text, f, blocks)
	if err != nil {
		return "", nil, "", err
	}
	return text, pdf, sum, nil
}

func signatoryName(p Party) string {
	if len(p.Signatories) > 0 {
		return p.Signatories[0].FullName
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// storePartyCopy нь хөлдсөн хувийг бичиж, талыг «илгээгдсэн» болгоно.
//
// Гарын үсэг зурагдсан хувийг ХӨНДӨХГҮЙ: зурагдсан бичвэрийг дарж бичих нь
// гарын үсгийг өөр баримт дээр буулгана.
func (m *DocumentsModule) storePartyCopy(ctx context.Context, tenantID, docID string,
	p Party, text string, pdf []byte, sum string) error {

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO document_party_files
		     (party_id, tenant_id, counterparty_tenant_id, document_id,
		      file_name, size_bytes, sha256, content, body_text)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (party_id) DO UPDATE
		   SET file_name = EXCLUDED.file_name, size_bytes = EXCLUDED.size_bytes,
		       sha256 = EXCLUDED.sha256, content = EXCLUDED.content,
		       body_text = EXCLUDED.body_text, frozen_at = NOW()
		 WHERE document_party_files.signed_content IS NULL`,
		p.ID, tenantID, p.CounterpartyTenant, docID,
		fileName(p.RegistrationNumber, "contract"), len(pdf), sum, pdf, text); err != nil {
		return fmt.Errorf("freeze the party copy: %w", err)
	}

	// Гарчиг, төрөл, хугацаа нь ЭНД хөлдөнө — талын мөрөнд, баримтын мөрөнд
	// биш. Хүлээн авагч `document_records`-ыг хэзээ ч уншихгүй (00005), ба
	// тэдний харсан гарчиг нь гаргагч дараа нь гарчгаа заcахад
	// өөрчлөгдөхгүй байх нь тэдний зурсан зүйлийг үнэн байлгана.
	if _, err := tx.Exec(ctx,
		`UPDATE document_parties p
		    SET state = CASE WHEN p.state = 'draft' THEN 'invited' ELSE p.state END,
		        invited_at = COALESCE(p.invited_at, NOW()), updated_at = NOW(),
		        doc_title = r.title, doc_type = r.doc_type, doc_due_at = r.due_at
		   FROM document_records r
		  WHERE p.id = $1 AND p.tenant_id = $2 AND r.id = p.document_id`,
		p.ID, tenantID); err != nil {
		return fmt.Errorf("mark the party invited: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, detail)
		 VALUES ($1, $2, $3, 'invited', '')`, tenantID, p.ID, docID); err != nil {
		return fmt.Errorf("record the invitation event: %w", err)
	}
	return tx.Commit(ctx)
}

var errNotSent = errors.New("энэ талд гэрээ хараахан хүргэгдээгүй")

// partyCopyHandler нь хөлдсөн PDF-ийг өгнө.
func (m *DocumentsModule) partyCopyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")
	var name string
	var pdf []byte
	err := m.db.QueryRow(r.Context(),
		`SELECT file_name, content FROM document_party_files
		  WHERE party_id = $1 AND tenant_id = $2 AND document_id = $3`,
		partyID, tenantID, docID).Scan(&name, &pdf)
	if isNoRows(err) || len(pdf) == 0 {
		nexus.Error(w, http.StatusNotFound, errNotSent.Error())
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хувь уншигдсангүй")
		return
	}
	writePDF(w, name, pdf)
}

func (m *DocumentsModule) partySignedCopyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")
	var name string
	var pdf []byte
	err := m.db.QueryRow(r.Context(),
		`SELECT file_name, signed_content FROM document_party_files
		  WHERE party_id = $1 AND tenant_id = $2 AND document_id = $3`,
		partyID, tenantID, docID).Scan(&name, &pdf)
	if isNoRows(err) || len(pdf) == 0 {
		nexus.Error(w, http.StatusNotFound, "гарын үсэгтэй хувь хараахан алга")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хувь уншигдсангүй")
		return
	}
	writePDF(w, name, pdf)
}

// writePDF нь PDF-ийг ХАРУУЛАХААР буцаана — гэрээг унших гэж байгаа хүнд
// татахаас илүү харуулах нь зөв.
func writePDF(w http.ResponseWriter, name string, b []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// ─────────────────────────────────────────────── эргүүлж татах ба дахин нээх
//
// Эрхийн тодорхойлолт `documents.send`-ийг «илгээх, дахин илгээх, эргүүлж
// татах, дахин нээх» гэж нэрлэдэг байсан ч сүүлийн хоёр нь БАЙГААГҮЙ.
// Зарласан эрх нь байхгүй үйлдлийг нэрлэх нь эрхийг уншиж шийдвэр гаргаж буй
// админд худал хэлсэн хэрэг.

var (
	// ErrAlreadyExecuted — хүчин төгөлдөр болсон гэрээг эргүүлж татахгүй.
	// Талууд гарын үсгээ зурсан бол тэр нь баримт болсон; түүнийг зогсоох
	// зам нь ЦУЦЛАХ (terminate), нуух биш.
	ErrAlreadyExecuted = errors.New("энэ гэрээ хүчин төгөлдөр болсон — эргүүлж татахын оронд цуцална")
	ErrNotWithdrawn    = errors.New("зөвхөн эргүүлж татсан гэрээг дахин нээнэ")
)

// withdrawHandler нь илгээсэн гэрээг буцаана.
//
// ГАРЫН ҮСЭГ ЗУРСАН ТАЛЫГ ХӨНДӨХГҮЙ. Тэдний гарын үсэг бол болсон явдал:
// «withdrawn» гэж дарж бичих нь бүртгэлийг худал болгоно. Зөвхөн хариу
// өгөөгүй тал л хаагдана, ба тэдний холбоос унтарна.
func (m *DocumentsModule) withdrawHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")

	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	reason := strings.TrimSpace(req.Reason)

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	if shape.State == ContractExecuted {
		nexus.Error(w, http.StatusConflict, ErrAlreadyExecuted.Error())
		return
	}

	tx, err := m.db.Begin(r.Context())
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "эргүүлж татагдсангүй")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	if _, err := tx.Exec(r.Context(),
		`UPDATE document_parties
		    SET state = 'withdrawn', withdrawn_at = NOW(),
		        session_id = NULL, session_at = NULL, session_signatory_id = NULL,
		        updated_at = NOW()
		  WHERE document_id = $1 AND tenant_id = $2
		    AND party_role <> 'issuer'
		    AND state IN ('draft','invited','viewed')`, docID, tenantID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд хаагдсангүй")
		return
	}
	// Амьд холбоос үлдвэл эргүүлж татсан гэрээ уншигдсаар байна.
	if _, err := tx.Exec(r.Context(),
		`UPDATE document_invitations SET revoked_at = NOW()
		  WHERE document_id = $1 AND tenant_id = $2 AND revoked_at IS NULL`,
		docID, tenantID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "холбоосууд хаагдсангүй")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, actor_user_id, detail)
		 SELECT $2, p.id, $1, 'withdrawn', NULLIF($3,'')::uuid, $4
		   FROM document_parties p
		  WHERE p.document_id = $1 AND p.tenant_id = $2 AND p.state = 'withdrawn'`,
		docID, tenantID, actorFor(r.Context()), reason); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "түүх бичигдсэнгүй")
		return
	}
	// ХАМГИЙН СҮҮЛД: талуудын trigger нь энэ баганыг дахин тоолдог тул
	// эцсийн үг нь эндхийнх байх ёстой.
	if _, err := tx.Exec(r.Context(),
		`UPDATE document_records
		    SET contract_state = $3, withdrawn_at = NOW()
		  WHERE id = $1 AND tenant_id = $2`, docID, tenantID, ContractWithdrawn); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гэрээний төлөв шинэчлэгдсэнгүй")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "эргүүлж татагдсангүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.contract_withdrawn", docID,
		map[string]any{"reason": reason})
	nexus.JSON(w, http.StatusOK, map[string]any{"contract_state": ContractWithdrawn})
}

// reopenHandler нь эргүүлж татсан гэрээг ноорог болгож буцаана.
//
// ХӨЛДСӨН ХУВИЙГ УСТГАНА. Дахин нээсэн гэрээ засагдана; хуучин байт үлдвэл
// дараагийн илгээлт «аль хэдийн хүргэгдсэн» гэж уншигдаад ХУУЧИН бичвэрийг
// шинэ талд өгнө.
func (m *DocumentsModule) reopenHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	if shape.State != ContractWithdrawn {
		nexus.Error(w, http.StatusConflict, ErrNotWithdrawn.Error())
		return
	}

	tx, err := m.db.Begin(r.Context())
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "дахин нээгдсэнгүй")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	// Гарын үсэг зурсан ба татгалзсан талыг ХӨНДӨХГҮЙ: аль аль нь болсон
	// явдал. Зөвхөн хаагдсан талууд ноорог болно.
	if _, err := tx.Exec(r.Context(),
		`UPDATE document_parties
		    SET state = 'draft', withdrawn_at = NULL, invited_at = NULL, viewed_at = NULL,
		        updated_at = NOW()
		  WHERE document_id = $1 AND tenant_id = $2 AND state = 'withdrawn'`,
		docID, tenantID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд нээгдсэнгүй")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`DELETE FROM document_party_files f
		  USING document_parties p
		  WHERE p.id = f.party_id AND p.document_id = $1 AND p.tenant_id = $2
		    AND p.state = 'draft' AND f.signed_content IS NULL`,
		docID, tenantID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хөлдсөн хувь цэвэрлэгдсэнгүй")
		return
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE document_records
		    SET contract_state = $3, withdrawn_at = NULL, sent_at = NULL
		  WHERE id = $1 AND tenant_id = $2`, docID, tenantID, ContractDraft); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гэрээний төлөв шинэчлэгдсэнгүй")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "дахин нээгдсэнгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.contract_reopened", docID, nil)
	nexus.JSON(w, http.StatusOK, map[string]any{"contract_state": ContractDraft})
}
