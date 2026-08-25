/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ХҮЛЭЭН АВАГЧИЙН ТАЛ.
//
// Гэрээ хүлээж авсан хүн эсвэл байгууллага өөрийн хувийг эндээс хардаг. Энэ
// нь гаргагчийн `/{id}/parties/...` гадаргууны толь БИШ, гурван зүйлээр:
//
//  1. ХҮРЭХ ЭРХ өөр асуултаас гарна. Гаргагчийн зам «энэ баримт миний
//     байгууллагынх уу» гэж асуудаг. Энэ зам «энэ ТАЛ надад хамаатай юу»
//     гэж асууна — гурван замын аль нэгээр: хүлээн авагч байгууллага,
//     нэрлэгдсэн хэрэглэгч, эсвэл гарын үсэг зурагчийн мөр.
//
//  2. ХАРАГДАХ ЗҮЙЛ нь нарийн. Гаргагчийн хариулт нь тал бүрийн утас,
//     и-мэйл, хаягийг агуулна. Нэг талын холбоо барих мэдээлэл нөгөөд
//     харагдах нь гэрээний тал байхын үр дагавар биш, тиймээс энд зөвхөн
//     нэр, үүрэг, төлөв.
//
//  3. `document_records`-ыг ЭНД ХЭЗЭЭ Ч УНШИХГҮЙ. Тэр хүснэгт гаргагчийнх
//     бөгөөд түүний RLS нь хүлээн авагчийг оруулдаггүй — ба оруулах ч
//     ёсгүй. Хүлээн авагчид хэрэгтэй гарчиг, төрөл, хугацаа нь илгээх
//     агшинд ТАЛЫН мөрөнд хөлддөг (00005). Тэр нь бас илүү үнэн: тэдний
//     харсан гарчиг нь гаргагч дараа нь гарчгаа заcахад өөрчлөгдөхгүй.
//
// Аль ч замаар хүрэхгүй мөрийг ОГТ БАЙХГҮЙ мэт хариулна: «байгаа гэхдээ
// чинийх биш» гэсэн хариулт нь таамаглаж буй хүнд тийм гэрээ БАЙДГИЙГ
// хэлнэ. Тиймээс энэ файлд 403 гэж байхгүй, зөвхөн 404.
package documents

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// inboxItem нь хүлээн авагчийн жагсаалтын мөр.
type inboxItem struct {
	PartyID    string     `json:"party_id"`
	DocumentID string     `json:"document_id"`
	Title      string     `json:"title"`
	DocType    string     `json:"doc_type"`
	Role       string     `json:"party_role"`
	IssuerName string     `json:"issuer_name"`
	IssuerReg  string     `json:"issuer_registration,omitempty"`
	State      string     `json:"state"`
	InvitedAt  *time.Time `json:"invited_at,omitempty"`
	DueAt      *time.Time `json:"due_at,omitempty"`
	HasCopy    bool       `json:"has_copy"`
	HasSigned  bool       `json:"has_signed_copy"`
}

// inboxScope нь «энэ хүн юунд хүрч болох вэ» гэдгийн SQL хэлбэр.
//
// Гурван замыг НЭГ предикатад барина: аль нэгийг мартвал зарим хүлээн авагч
// өөрийн гэрээгээ олохгүй, эсвэл өөр хүний гэрээг олно. Тиймээс нэг л газарт
// бичигдэж, хүрэх эрх шалгадаг бүх query түүнийг хуваана.
//
// $1 = идэвхтэй байгууллага, $2 = нэвтэрсэн хэрэглэгч. Сангийн RLS нь үүний
// ард зогсох ба хоёулаа зөрвөл мөр буцахгүй — энэ предикат бол зорилго,
// бодлого бол баталгаа.
const inboxScope = `(
     p.counterparty_tenant_id = $1
  OR p.member_user_id = NULLIF($2,'')::uuid
  OR EXISTS (SELECT 1 FROM document_party_signatories g
              WHERE g.party_id = p.id AND g.user_id = NULLIF($2,'')::uuid)
)`

// inboxOpenStates нь «миний хариу хүлээж буй» гэдгийн тодорхойлолт.
var inboxOpenStates = []string{PartyInvited, PartyViewed}

// inboxAllStates нь түүх. Хүн өөрийн зурсан гэрээгээ дараа нь олох ёстой.
var inboxAllStates = []string{
	PartyInvited, PartyViewed, PartySigned, PartyDeclined, PartyWithdrawn, PartyExpired,
}

func (m *DocumentsModule) inboxHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	claims, err := nexus.UserFromContext(r.Context())
	if err != nil {
		nexus.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	states := inboxOpenStates
	if strings.TrimSpace(r.URL.Query().Get("state")) == "all" {
		states = inboxAllStates
	}

	// Гаргагчийн нэр нь ТАЛУУДЫН хүснэгтээс — `issuer` үүрэгтэй мөрөөс.
	// Тэр мөр нь `parties_see_each_other` бодлогоор нээгддэг ба тэр бодлого
	// нь `audience`-аас уншдаг тул илгээх агшинд бичигдсэн байх ёстой
	// (`sendHandler`-ийг үз).
	rows, err := m.db.Query(r.Context(),
		`SELECT p.id, p.document_id, p.doc_title, p.doc_type, p.party_role,
		        COALESCE(i.display_name, ''), COALESCE(i.registration_number, ''),
		        p.state, p.invited_at, p.doc_due_at,
		        EXISTS (SELECT 1 FROM document_party_files f
		                 WHERE f.party_id = p.id AND f.content IS NOT NULL),
		        EXISTS (SELECT 1 FROM document_party_files f
		                 WHERE f.party_id = p.id AND f.signed_content IS NOT NULL)
		   FROM document_parties p
		   LEFT JOIN document_parties i
		          ON i.document_id = p.document_id AND i.party_role = 'issuer'
		  WHERE `+inboxScope+` AND p.party_role <> 'issuer' AND p.state = ANY($3)
		  ORDER BY p.invited_at DESC NULLS LAST, p.id`,
		tenantID, claims.UserID, states)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "жагсаалт уншигдсангүй")
		return
	}
	defer rows.Close()

	items := []inboxItem{}
	for rows.Next() {
		var v inboxItem
		if err := rows.Scan(&v.PartyID, &v.DocumentID, &v.Title, &v.DocType, &v.Role,
			&v.IssuerName, &v.IssuerReg, &v.State, &v.InvitedAt, &v.DueAt,
			&v.HasCopy, &v.HasSigned); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "мөр уншигдсангүй")
			return
		}
		items = append(items, v)
	}
	// Хэсэгчилсэн жагсаалт нь хүлээн авагчид «гэрээ алга» гэж уншигдана —
	// тэр бол хамгийн буруу хариулт.
	if err := rows.Err(); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "жагсаалт бүрэн уншигдсангүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// inboxParty нь дуудагчийн хүрч чадах талын хамгийн бага хэлбэр.
type inboxParty struct {
	ID       string
	DocID    string
	Owner    string // гаргагчийн байгууллага — хөлдсөн хувь тэнд хадгалагдана
	State    string
	Title    string
	DocType  string
	Required bool
}

// reachInbox нь дуудагч тухайн талд хүрч болохыг батална.
//
// Олдохгүй ба эрхгүй хоёрыг НЭГ хариултаар буцаана. Дуудагч бүр үүнийг
// ЭХЭЛЖ дуудна — `pid` нь хаягийн мөрөөс ирдэг тул шалгаагүй бол дурын
// талын гэрээг уншина.
func (m *DocumentsModule) reachInbox(r *http.Request, tenantID, partyID string) (inboxParty, bool) {
	var v inboxParty
	claims, err := nexus.UserFromContext(r.Context())
	if err != nil {
		return v, false
	}
	err = m.db.QueryRow(r.Context(),
		`SELECT p.id, p.document_id, p.tenant_id::text, p.state, p.doc_title, p.doc_type, p.required
		   FROM document_parties p
		  WHERE p.id = $3 AND p.party_role <> 'issuer' AND `+inboxScope,
		tenantID, claims.UserID, partyID).
		Scan(&v.ID, &v.DocID, &v.Owner, &v.State, &v.Title, &v.DocType, &v.Required)
	if err != nil {
		return v, false
	}
	return v, true
}

// inboxBrief нь гэрээнд ХЭН байгааг хэлнэ, тэдэнд хэрхэн хүрэхийг биш.
type inboxBrief struct {
	Role  string `json:"party_role"`
	Name  string `json:"display_name"`
	State string `json:"state"`
	Mine  bool   `json:"mine"`
}

func (m *DocumentsModule) inboxShowHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}

	var bodyText, sha string
	var frozenAt *time.Time
	err := m.db.QueryRow(r.Context(),
		`SELECT COALESCE(body_text, ''), COALESCE(sha256, ''), frozen_at
		   FROM document_party_files WHERE party_id = $1`, party.ID).
		Scan(&bodyText, &sha, &frozenAt)
	if err != nil && !isNoRows(err) {
		nexus.Error(w, http.StatusInternalServerError, "хувь уншигдсангүй")
		return
	}

	rows, err := m.db.Query(r.Context(),
		`SELECT id, party_role, display_name, state FROM document_parties
		  WHERE document_id = $1 ORDER BY ordinal`, party.DocID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд уншигдсангүй")
		return
	}
	defer rows.Close()
	others := []inboxBrief{}
	for rows.Next() {
		var id string
		var b inboxBrief
		if err := rows.Scan(&id, &b.Role, &b.Name, &b.State); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "мөр уншигдсангүй")
			return
		}
		b.Mine = id == party.ID
		others = append(others, b)
	}
	if err := rows.Err(); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд бүрэн уншигдсангүй")
		return
	}

	mine, err := m.signatoriesOf(r, party.ID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гарын үсэг зурагчид уншигдсангүй")
		return
	}

	// Уншсаныг ЭНД бүртгэнэ — хариу буцахаас өмнө, тусдаа дуудлагагүйгээр.
	state, err := m.markViewed(r, party)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "уншсан тэмдэглэл бичигдсэнгүй")
		return
	}

	nexus.JSON(w, http.StatusOK, map[string]any{
		"party_id": party.ID, "document_id": party.DocID,
		"title": party.Title, "doc_type": party.DocType, "state": state,
		"required": party.Required,
		"body_text": bodyText, "sha256": sha, "frozen_at": frozenAt,
		"has_copy": sha != "",
		"parties":  others, "my_signatories": mine,
	})
}

func (m *DocumentsModule) signatoriesOf(r *http.Request, partyID string) ([]Signatory, error) {
	rows, err := m.db.Query(r.Context(),
		`SELECT id, party_id, full_name, position, reg_number, user_id::text, signed_at
		   FROM document_party_signatories WHERE party_id = $1 ORDER BY created_at`, partyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Signatory{}
	for rows.Next() {
		var s Signatory
		if err := rows.Scan(&s.ID, &s.PartyID, &s.FullName, &s.Position, &s.RegNumber,
			&s.UserID, &s.SignedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// markViewed нь «хүлээн авагч гэрээгээ уншив» гэдгийг бүртгэнэ.
//
// Энэ нь ТУСДАА `POST /open` маршрут БАЙХГҮЙ: тийм маршрут нь бичих үйлдэл
// байсаар байж зөвхөн унших эрх шаардах ба — үүнээс ч дор — клиент дуудахаа
// мартвал бүртгэл нь чимээгүй худал болно. Гэрээний бичвэрийг АВЧ ҮЗЭХ нь
// уншсаны цорын ганц үнэн баталгаа, тиймээс тэр агшинд бичигдэнэ.
//
// Идемпотент: төлөв `invited` байхад л ажиллана. Түүхийн мөр НЭГ л удаа —
// дахин нээх бүрд мөр нэмбэл «хэзээ уншив» гэдгийн хариулт мянган мөр болно.
func (m *DocumentsModule) markViewed(r *http.Request, party inboxParty) (string, error) {
	if party.State != PartyInvited {
		return party.State, nil
	}
	tx, err := m.db.Begin(r.Context())
	if err != nil {
		return party.State, err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	tag, err := tx.Exec(r.Context(),
		`UPDATE document_parties
		    SET state = 'viewed', viewed_at = COALESCE(viewed_at, NOW()), updated_at = NOW()
		  WHERE id = $1 AND state = 'invited'`, party.ID)
	if err != nil {
		return party.State, err
	}
	if tag.RowsAffected() == 0 {
		return party.State, nil // өөр хүсэлт түрүүлсэн
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, detail)
		 VALUES ($1, $2, $3, 'viewed', '')`, party.Owner, party.ID, party.DocID); err != nil {
		return party.State, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return party.State, err
	}
	return PartyViewed, nil
}

// inboxCopyHandler нь хүлээн авагчийн хөлдсөн хувийг өгнө; `signed.pdf` нь
// тэдний буцаасан, гарын үсэгтэй хувийг.
func (m *DocumentsModule) inboxCopyHandler(w http.ResponseWriter, r *http.Request) {
	m.serveInboxCopy(w, r, "content", errNotSent.Error())
}

func (m *DocumentsModule) inboxSignedCopyHandler(w http.ResponseWriter, r *http.Request) {
	m.serveInboxCopy(w, r, "signed_content", "гарын үсэгтэй хувь хараахан алга")
}

// serveInboxCopy — баганын нэр нь ЭНД тогтмол, хүсэлтээс ХЭЗЭЭ Ч ирэхгүй.
func (m *DocumentsModule) serveInboxCopy(w http.ResponseWriter, r *http.Request, column, missing string) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	var name string
	var pdf []byte
	err := m.db.QueryRow(r.Context(),
		`SELECT file_name, `+column+` FROM document_party_files WHERE party_id = $1`,
		party.ID).Scan(&name, &pdf)
	if isNoRows(err) || len(pdf) == 0 {
		nexus.Error(w, http.StatusNotFound, missing)
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хувь уншигдсангүй")
		return
	}
	writePDF(w, name, pdf)
}

// inboxSignatoriesHandler нь хүлээн авагчийн бүртгэсэн хүмүүсийг өгнө.
func (m *DocumentsModule) inboxSignatoriesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	list, err := m.signatoriesOf(r, party.ID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гарын үсэг зурагчид уншигдсангүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"signatories": list})
}

// inboxNominateHandler нь хүлээн авагч ӨӨРИЙН гарын үсэг зурагчаа нэрлэнэ.
//
// ШИЙДВЭР: гэрээ хүлээн авсан байгууллагын өмнөөс хэн гарын үсэг зурахыг
// ГАРГАГЧ шийддэггүй. Гаргагч талыг нэрлэдэг — хуулийн этгээдийг — харин
// тэр этгээдийн доторх эрх бүхий хүнийг зөвхөн тэд өөрсдөө мэднэ. Гаргагчид
// зөвшөөрвөл гарын үсэг зурах эрх нь гэрээ явуулж чадах хүн бүрийн бэлэг
// болно.
//
// Тиймээс энэ маршрут `documents.parties` эрх шаардана — ХҮЛЭЭН АВАГЧИЙН
// байгууллагад. Эрхийн шалгалт нь идэвхтэй байгууллагын гишүүнчлэлээс
// гардаг тул тэр нь өөрөө зөв тенантад асуугдана.
func (m *DocumentsModule) inboxNominateHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	if party.State != PartyInvited && party.State != PartyViewed {
		nexus.Error(w, http.StatusConflict, ErrPartySettled.Error())
		return
	}

	var req struct {
		FullName  string  `json:"full_name"`
		Position  string  `json:"position"`
		RegNumber string  `json:"reg_number"`
		UserID    *string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "хүсэлт уншигдсангүй")
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.RegNumber = strings.ToUpper(strings.TrimSpace(req.RegNumber))
	if req.FullName == "" {
		nexus.Error(w, http.StatusBadRequest, "овог нэрийг бичнэ үү — гэрээнд хэвлэгдэнэ")
		return
	}
	if req.RegNumber == "" {
		// Дугааргүй бол PIN2 хүсэлт хэнд ч очихгүй: рельс иргэнийг
		// регистрийн дугаараар нь олдог.
		nexus.Error(w, http.StatusBadRequest, "регистрийн дугаарыг бичнэ үү — PIN2 хүсэлт түүгээр очно")
		return
	}

	// `tenant_id` ба `counterparty_tenant_id` хоёрыг сангийн trigger талаас
	// нь хуулна (00002) — гараар бичих зам байхгүй.
	var s Signatory
	err := m.db.QueryRow(r.Context(),
		`INSERT INTO document_party_signatories
		     (tenant_id, party_id, document_id, full_name, position, reg_number, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, party_id, full_name, position, reg_number, user_id::text, signed_at`,
		tenantID, party.ID, party.DocID, req.FullName, strings.TrimSpace(req.Position),
		req.RegNumber, req.UserID).
		Scan(&s.ID, &s.PartyID, &s.FullName, &s.Position, &s.RegNumber, &s.UserID, &s.SignedAt)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "гарын үсэг зурагч бүртгэгдсэнгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.inbox_signatory_named",
		party.DocID, map[string]any{"party_id": party.ID, "reg_number": req.RegNumber})
	nexus.JSON(w, http.StatusCreated, s)
}

// inboxWithdrawNomineeHandler нь бүртгэсэн, гарын үсэг зураагүй хүнийг хасна.
func (m *DocumentsModule) inboxWithdrawNomineeHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	// Гарын үсэг зурсан хүнийг хасах нь бүртгэлийг арилгах гэсэн үг.
	tag, err := m.db.Exec(r.Context(),
		`DELETE FROM document_party_signatories
		  WHERE id = $1 AND party_id = $2 AND signed_at IS NULL`,
		chi.URLParam(r, "sid"), party.ID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хасагдсангүй")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusNotFound, "гарын үсэг зураагүй ийм хүн олдсонгүй")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────── хүлээн авагчийн PIN2 ёслол
//
// Гаргагчийн талынхтай ЯГ ижил ёслол ажиллана — `signerFor`,
// `startPartySignature`, `pollPartySignature` — ялгаа нь зөвхөн хүрэх эрхийг
// хэрхэн батлахад ба ёслолын мөрүүд ГАРГАГЧИЙН байгууллагад
// хадгалагддагт. Хоёр өөр ёслол бичих нь хоёр өөр алдаа бичих гэсэн үг.

func (m *DocumentsModule) inboxSignStartHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	claims, _ := nexus.UserFromContext(r.Context())

	s, err := m.signerFor(r.Context(), party.Owner, party.DocID, party.ID, claims.UserID)
	switch {
	case errors.Is(err, ErrNoSignatory), errors.Is(err, ErrNoPartyCopy):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	session, err := m.startPartySignature(r.Context(), s, claims.UserID)
	switch {
	case errors.Is(err, ErrCeremonyOpen):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrNoSigningRail):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.inbox_sign_started",
		party.DocID, map[string]any{"party_id": party.ID, "session_id": session.SessionID})
	nexus.JSON(w, http.StatusOK, session)
}

func (m *DocumentsModule) inboxSignPollHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	progress, err := m.pollPartySignature(r.Context(), party.Owner, party.DocID, party.ID)
	switch {
	case errors.Is(err, ErrBytesChanged), errors.Is(err, ErrPartySettled):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrNoSigningRail):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, ErrPartyNotFound):
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	case err != nil:
		nexus.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if progress.State == ApprovalComplete {
		nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.inbox_signed",
			party.DocID, map[string]any{"party_id": party.ID})
	}
	nexus.JSON(w, http.StatusOK, progress)
}

// inboxDeclineHandler — татгалзал нь eID-ийн үйлдэл биш, бизнесийн шийдвэр.
func (m *DocumentsModule) inboxDeclineHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	party, ok := m.reachInbox(r, tenantID, chi.URLParam(r, "pid"))
	if !ok {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		// Шалтгаангүй татгалзал гаргагчид «юу засах вэ» гэдгийг хэлэхгүй.
		nexus.Error(w, http.StatusBadRequest, "татгалзсан шалтгаанаа бичнэ үү")
		return
	}
	reason := strings.TrimSpace(req.Reason)

	tx, err := m.db.Begin(r.Context())
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	tag, err := tx.Exec(r.Context(),
		`UPDATE document_parties
		    SET state = 'declined', declined_at = NOW(), decline_reason = $2,
		        session_id = NULL, session_at = NULL, session_signatory_id = NULL,
		        updated_at = NOW()
		  WHERE id = $1 AND state IN ('invited','viewed')`, party.ID, reason)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusConflict, ErrPartySettled.Error())
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, detail)
		 VALUES ($1, $2, $3, 'declined', $4)`,
		party.Owner, party.ID, party.DocID, reason); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.inbox_declined",
		party.DocID, map[string]any{"party_id": party.ID})
	nexus.JSON(w, http.StatusOK, map[string]any{"state": PartyDeclined})
}
