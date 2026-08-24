/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// Гэрээний ТАЛУУД.
//
// Энэ файл нь «хэнтэй гэрээ байгуулж байна вэ» гэдэг асуултын хариулт. Түүнээс
// өмнө энэ апп зөвхөн «ямар баримт байна вэ» гэдэгт хариулдаг байсан:
// `document_workflow_steps` нь `(tenant_id, doc_type, step_order)`-оор
// түлхүүрлэгддэг тул зөвшөөрлийн дараалал нь БАРИМТЫН ТӨРӨЛ тутамд нэг, бүх
// гэрээ түүнийг хуваадаг. Тодорхой гэрээний тодорхой хоёр талыг тэр загварт
// илэрхийлэх боломжгүй.
//
// # ХОЁР ЗҮЙЛИЙГ ЯЛГАВ, ЗОРИУДААР
//
// Тал (`document_parties`) нь ХУУЛИЙН ЭТГЭЭД: компани, байгууллага, иргэн.
// Гарын үсэг зурагч (`document_party_signatories`) нь ХҮН. Хоёрыг нэг мөрөнд
// нийлүүлбэл гарын үсэг зурах эрх нь «албан тушаал» гэсэн чөлөөт бичвэрээс
// гарах ба тэр бичвэрийг засаж чадах хүн бүр өөрийгөө эрхтэй болгоно. eduge
// тэр өртгийг нэг удаа төлсөн.
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

// Талын төлөв. Гэрээний амьдрал `document_records.status`-аас ТУСДАА явдаг:
// тэр багана нь дотоод зөвшөөрлийн дараалалд хариулдаг бөгөөд түүнийг
// `nexus.FiledDocument` дамжуулан өөр репо дахь клиентүүд уншдаг.
const (
	PartyDraft     = "draft"
	PartyInvited   = "invited"
	PartyViewed    = "viewed"
	PartySigned    = "signed"
	PartyDeclined  = "declined"
	PartyWithdrawn = "withdrawn"
	PartyExpired   = "expired"
)

// Талын үүрэг ба төрөл.
const (
	RoleIssuer       = "issuer"
	RoleCounterparty = "counterparty"
	RoleWitness      = "witness"
	RoleGuarantor    = "guarantor"

	// KindMember — энэ тенантын хэрэглэгч. KindTenant — энэ суулгац дээрх өөр
	// байгууллага. KindPeer — өөр суулгац. KindPerson/KindOrganisation — данс
	// байхгүй, урилгын токеноор ирнэ.
	KindMember       = "member"
	KindTenant       = "tenant"
	KindPeer         = "peer"
	KindPerson       = "person"
	KindOrganisation = "organisation"
)

// Гэрээний нэгтгэсэн төлөв (`document_records.contract_state`).
const (
	ContractNone      = "NONE"
	ContractDraft     = "DRAFT"
	ContractSent      = "SENT"
	ContractPartial   = "PARTIALLY_SIGNED"
	ContractExecuted  = "EXECUTED"
	ContractDeclined  = "DECLINED"
	ContractWithdrawn = "WITHDRAWN"
)

// Гарын үсгийн горим.
const (
	ModeInternal    = "internal"
	ModeCounterpart = "counterpart"
	ModeJoint       = "joint"
)

var (
	ErrNotDraft      = errors.New("гэрээ ноорог төлөвт байхаа больсон")
	ErrPartyNotFound = errors.New("тал олдсонгүй")
	ErrPartySettled  = errors.New("энэ тал аль хэдийн шийдвэрээ өгсөн")
	ErrForeignParty  = errors.New("энэ тал өөр байгууллагынх — гарын үсэг зурагчаа тэд өөрсдөө нэрлэнэ")
	ErrNoSignatory   = errors.New("энэ талд гарын үсэг зурах эрх бүхий хүн бүртгэгдээгүй")
	ErrCeremonyOpen  = errors.New("энэ тал дээр гарын үсгийн ёслол нээлттэй байна")
)

// ceremonyWindow нь нээлттэй ёслол мөрийг түгжих хугацаа.
//
// Платформ өөрөө pending ёслолыг 20 минутын дараа хугацаа дуусгадаг тул
// түүнээс хойш шинийг зөвшөөрнө — эс бөгөөс орхигдсон ёслол талыг үүрд
// түгжинэ.
const ceremonyWindow = 20 * time.Minute

// Party нь гадагш харагдах хэлбэр.
//
// Холбоо барих мэдээлэл нь ЗӨВХӨН гаргагчид харагдана: `inbox` хариултууд
// үүнийг ашигладаггүй (`inboxParty`-г үз). Нэг талын утас нөгөөд харагдах нь
// гэрээний тал байхын үр дагавар биш.
type Party struct {
	ID                 string      `json:"id"`
	Ordinal            int         `json:"ordinal"`
	Role               string      `json:"party_role"`
	Kind               string      `json:"party_kind"`
	DisplayName        string      `json:"display_name"`
	LegalName          string      `json:"legal_name,omitempty"`
	RegistrationNumber string      `json:"registration_number,omitempty"`
	AddressLine        string      `json:"address_line,omitempty"`
	ContactEmail       string      `json:"contact_email,omitempty"`
	ContactPhone       string      `json:"contact_phone,omitempty"`
	MemberUserID       *string     `json:"member_user_id,omitempty"`
	CounterpartyTenant *string     `json:"counterparty_tenant_id,omitempty"`
	Required           bool        `json:"required"`
	SignOrder          *int        `json:"sign_order,omitempty"`
	State              string      `json:"state"`
	InvitedAt          *time.Time  `json:"invited_at,omitempty"`
	ViewedAt           *time.Time  `json:"viewed_at,omitempty"`
	SignedAt           *time.Time  `json:"signed_at,omitempty"`
	DeclinedAt         *time.Time  `json:"declined_at,omitempty"`
	WithdrawnAt        *time.Time  `json:"withdrawn_at,omitempty"`
	DeclineReason      string      `json:"decline_reason,omitempty"`
	Signatories        []Signatory `json:"signatories,omitempty"`
	HasCopy            bool        `json:"has_copy"`
	HasSignedCopy      bool        `json:"has_signed_copy"`
}

// Signatory нь тал бүрийн өмнөөс гарын үсэг зурах эрхтэй ХҮН.
type Signatory struct {
	ID        string     `json:"id"`
	PartyID   string     `json:"party_id"`
	FullName  string     `json:"full_name"`
	Position  string     `json:"position,omitempty"`
	RegNumber string     `json:"reg_number,omitempty"`
	UserID    *string    `json:"user_id,omitempty"`
	SignedAt  *time.Time `json:"signed_at,omitempty"`
}

const partyColumns = `p.id, p.ordinal, p.party_role, p.party_kind, p.display_name,
       p.legal_name, p.registration_number, p.address_line, p.contact_email, p.contact_phone,
       p.member_user_id::text, p.counterparty_tenant_id::text,
       p.required, p.sign_order, p.state,
       p.invited_at, p.viewed_at, p.signed_at, p.declined_at, p.withdrawn_at, p.decline_reason,
       EXISTS (SELECT 1 FROM document_party_files f WHERE f.party_id = p.id AND f.content IS NOT NULL),
       EXISTS (SELECT 1 FROM document_party_files f WHERE f.party_id = p.id AND f.signed_content IS NOT NULL)`

func scanParty(scan func(...any) error) (Party, error) {
	var v Party
	err := scan(&v.ID, &v.Ordinal, &v.Role, &v.Kind, &v.DisplayName,
		&v.LegalName, &v.RegistrationNumber, &v.AddressLine, &v.ContactEmail, &v.ContactPhone,
		&v.MemberUserID, &v.CounterpartyTenant,
		&v.Required, &v.SignOrder, &v.State,
		&v.InvitedAt, &v.ViewedAt, &v.SignedAt, &v.DeclinedAt, &v.WithdrawnAt, &v.DeclineReason,
		&v.HasCopy, &v.HasSignedCopy)
	return v, err
}

// PartiesOf нь гэрээний бүх талыг гарын үсэг зурагчидтай нь хамт уншина.
func (m *DocumentsModule) PartiesOf(ctx context.Context, tenantID, docID string) ([]Party, error) {
	rows, err := m.db.Query(ctx,
		`SELECT `+partyColumns+` FROM document_parties p
		  WHERE p.tenant_id = $1 AND p.document_id = $2 ORDER BY p.ordinal`, tenantID, docID)
	if err != nil {
		return nil, fmt.Errorf("read parties: %w", err)
	}
	defer rows.Close()
	parties := []Party{}
	index := map[string]int{}
	for rows.Next() {
		v, err := scanParty(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("read a party row: %w", err)
		}
		index[v.ID] = len(parties)
		parties = append(parties, v)
	}
	// Хэсэгчилсэн жагсаалт нь «энэ гэрээнд ийм тал байхгүй» гэж уншигдана.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read parties: %w", err)
	}
	if len(parties) == 0 {
		return parties, nil
	}

	sigs, err := m.db.Query(ctx,
		`SELECT s.id, s.party_id, s.full_name, s.position, s.reg_number, s.user_id::text, s.signed_at
		   FROM document_party_signatories s
		   JOIN document_parties p ON p.id = s.party_id
		  WHERE p.tenant_id = $1 AND p.document_id = $2
		  ORDER BY s.created_at`, tenantID, docID)
	if err != nil {
		return nil, fmt.Errorf("read signatories: %w", err)
	}
	defer sigs.Close()
	for sigs.Next() {
		var s Signatory
		if err := sigs.Scan(&s.ID, &s.PartyID, &s.FullName, &s.Position, &s.RegNumber,
			&s.UserID, &s.SignedAt); err != nil {
			return nil, fmt.Errorf("read a signatory row: %w", err)
		}
		if i, ok := index[s.PartyID]; ok {
			parties[i].Signatories = append(parties[i].Signatories, s)
		}
	}
	if err := sigs.Err(); err != nil {
		return nil, fmt.Errorf("read signatories: %w", err)
	}
	return parties, nil
}

// contractShape нь гэрээний төлөв ба горимыг нэг уншилтаар авчирна.
type contractShape struct {
	Mode    string
	State   string
	Status  string
	DocType string
	Title   string
	Signed  int
}

func (m *DocumentsModule) contractShapeOf(ctx context.Context, q querier, tenantID, docID string) (contractShape, error) {
	var s contractShape
	err := q.QueryRow(ctx,
		`SELECT r.signing_mode, r.contract_state, r.status, r.doc_type, r.title,
		        (SELECT count(*) FROM document_signatures g WHERE g.document_id = r.id)
		   FROM document_records r WHERE r.id = $1 AND r.tenant_id = $2`,
		docID, tenantID).Scan(&s.Mode, &s.State, &s.Status, &s.DocType, &s.Title, &s.Signed)
	if isNoRows(err) {
		return s, ErrNotSignable
	}
	if err != nil {
		return s, fmt.Errorf("read contract shape: %w", err)
	}
	return s, nil
}

// ─────────────────────────────────────────────────────────────── handler-ууд

func (m *DocumentsModule) listPartiesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")
	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	}
	parties, err := m.PartiesOf(r.Context(), tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд уншигдсангүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{
		"parties": parties, "mode": shape.Mode, "contract_state": shape.State,
	})
}

// partyRequest нь тал үүсгэх, засах хүсэлт.
type partyRequest struct {
	Role               string  `json:"party_role"`
	Kind               string  `json:"party_kind"`
	DisplayName        string  `json:"display_name"`
	LegalName          string  `json:"legal_name"`
	RegistrationNumber string  `json:"registration_number"`
	AddressLine        string  `json:"address_line"`
	ContactEmail       string  `json:"contact_email"`
	ContactPhone       string  `json:"contact_phone"`
	Required           *bool   `json:"required"`
	SignOrder          *int    `json:"sign_order"`
	MemberUserID       *string `json:"member_user_id"`
	CounterpartyTenant *string `json:"counterparty_tenant_id"`
}

func (p partyRequest) validate() error {
	switch p.Role {
	case RoleIssuer, RoleCounterparty, RoleWitness, RoleGuarantor:
	default:
		return fmt.Errorf("талын үүрэг танигдахгүй байна: %q", p.Role)
	}
	switch p.Kind {
	case KindMember, KindTenant, KindPeer, KindPerson, KindOrganisation:
	default:
		return fmt.Errorf("талын төрөл танигдахгүй байна: %q", p.Kind)
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		return errors.New("талын нэрийг бичнэ үү")
	}
	// Байгууллага гэдэг нь регистрийн дугаараар танигддаг. Түүнгүйгээр
	// хоёр өөр компани нэг нэртэй байхад гэрээ хэнтэй байгуулагдсаныг
	// баримт өөрөө хэлж чадахгүй.
	if (p.Kind == KindOrganisation || p.Kind == KindTenant) &&
		strings.TrimSpace(p.RegistrationNumber) == "" {
		return errors.New("байгууллагын регистрийн дугаарыг бичнэ үү")
	}
	return nil
}

func (m *DocumentsModule) addPartyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")

	var req partyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "хүсэлт уншигдсангүй")
		return
	}
	if err := req.validate(); err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	}
	// Талыг зөвхөн ноорог дээр нэмнэ: илгээгдсэний дараа тал нэмэх нь
	// аль хэдийн гарын үсэг зурсан хүмүүсийн зурсан зүйлийг өөрчилнө.
	if shape.State != ContractDraft && shape.State != ContractNone {
		nexus.Error(w, http.StatusConflict, ErrNotDraft.Error())
		return
	}
	if shape.Signed > 0 {
		nexus.Error(w, http.StatusConflict,
			"энэ баримтад гарын үсэг зурагдсан тул талууд өөрчлөгдөхгүй")
		return
	}

	party, err := m.insertParty(r.Context(), tenantID, docID, req, actorFor(r.Context()))
	switch {
	case errors.Is(err, errDuplicateParty):
		nexus.Error(w, http.StatusConflict, "энэ регистрийн дугаартай тал энэ гэрээнд аль хэдийн байна")
		return
	case err != nil:
		nexus.Error(w, http.StatusInternalServerError, "тал бүртгэгдсэнгүй")
		return
	}

	// Гэрээ болсон агшин: анхны тал нэмэгдэхэд төлөв нь NONE-оос DRAFT болно.
	if shape.State == ContractNone {
		if _, err := m.db.Exec(r.Context(),
			`UPDATE document_records SET contract_state = $3
			  WHERE id = $1 AND tenant_id = $2 AND contract_state = $4`,
			docID, tenantID, ContractDraft, ContractNone); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "гэрээний төлөв шинэчлэгдсэнгүй")
			return
		}
	}

	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.party_added", docID,
		map[string]any{"party_id": party.ID, "role": party.Role, "kind": party.Kind})
	nexus.JSON(w, http.StatusCreated, party)
}

var errDuplicateParty = errors.New("duplicate party")

func (m *DocumentsModule) insertParty(ctx context.Context, tenantID, docID string,
	req partyRequest, actor string) (Party, error) {

	required := true
	if req.Required != nil {
		required = *req.Required
	}
	var id string
	err := m.db.QueryRow(ctx,
		`INSERT INTO document_parties
		     (tenant_id, document_id, ordinal, party_role, party_kind, display_name,
		      legal_name, registration_number, address_line, contact_email, contact_phone,
		      member_user_id, counterparty_tenant_id, required, sign_order, added_by)
		 VALUES ($1, $2,
		     (SELECT COALESCE(max(ordinal), 0) + 1 FROM document_parties WHERE document_id = $2),
		     $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NULLIF($15,'')::uuid)
		 RETURNING id`,
		tenantID, docID, req.Role, req.Kind, strings.TrimSpace(req.DisplayName),
		strings.TrimSpace(req.LegalName), strings.TrimSpace(req.RegistrationNumber),
		strings.TrimSpace(req.AddressLine), strings.TrimSpace(req.ContactEmail),
		strings.TrimSpace(req.ContactPhone),
		req.MemberUserID, req.CounterpartyTenant, required, req.SignOrder, actor).Scan(&id)
	if isConstraintViolation(err, "document_parties_registration_unique") {
		return Party{}, errDuplicateParty
	}
	if err != nil {
		return Party{}, fmt.Errorf("insert party: %w", err)
	}
	return m.partyByID(ctx, tenantID, docID, id)
}

func (m *DocumentsModule) partyByID(ctx context.Context, tenantID, docID, partyID string) (Party, error) {
	v, err := scanParty(m.db.QueryRow(ctx,
		`SELECT `+partyColumns+` FROM document_parties p
		  WHERE p.tenant_id = $1 AND p.document_id = $2 AND p.id = $3`,
		tenantID, docID, partyID).Scan)
	if isNoRows(err) {
		return Party{}, ErrPartyNotFound
	}
	if err != nil {
		return Party{}, fmt.Errorf("read party: %w", err)
	}
	return v, nil
}

func (m *DocumentsModule) removePartyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")

	shape, err := m.contractShapeOf(r.Context(), m.db, tenantID, docID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, "document not found")
		return
	}
	if shape.State != ContractDraft && shape.State != ContractNone {
		nexus.Error(w, http.StatusConflict, ErrNotDraft.Error())
		return
	}
	tag, err := m.db.Exec(r.Context(),
		`DELETE FROM document_parties
		  WHERE tenant_id = $1 AND document_id = $2 AND id = $3 AND state = 'draft'`,
		tenantID, docID, partyID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "тал хасагдсангүй")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusNotFound, ErrPartyNotFound.Error())
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.party_removed", docID,
		map[string]any{"party_id": partyID})
	w.WriteHeader(http.StatusNoContent)
}

// addSignatoryHandler нь ӨӨРИЙН талын гарын үсэг зурагчийг нэрлэнэ.
//
// Өөр байгууллагын хүнийг гаргагч нэрлэхгүй: тэр байгууллага хэн өөрсдийнх нь
// өмнөөс гарын үсэг зурахыг өөрөө шийднэ (`/inbox/{pid}/signatories`). Хуулийн
// талаас ч мөн адил — та этгээдтэй гэрээ байгуулдаг, этгээд нь хэн гарын үсэг
// зурахаа өөрөө хэлдэг.
func (m *DocumentsModule) addSignatoryHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")

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
		// Нэр нь гэрээнд ХЭВЛЭГДЭНЭ. Хоосон нэртэй бүртгэл нь гарын үсэг
		// зурсан талын нэр хоосон гэрээ үлдээнэ.
		nexus.Error(w, http.StatusBadRequest, "овог нэрийг бичнэ үү — гэрээнд хэвлэгдэнэ")
		return
	}

	party, err := m.partyByID(r.Context(), tenantID, docID, partyID)
	if err != nil {
		nexus.Error(w, http.StatusNotFound, ErrPartyNotFound.Error())
		return
	}
	if party.CounterpartyTenant != nil {
		nexus.Error(w, http.StatusForbidden, ErrForeignParty.Error())
		return
	}
	if party.State == PartySigned || party.State == PartyDeclined {
		nexus.Error(w, http.StatusConflict, ErrPartySettled.Error())
		return
	}

	var s Signatory
	err = m.db.QueryRow(r.Context(),
		`INSERT INTO document_party_signatories
		     (tenant_id, party_id, full_name, position, reg_number, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, party_id, full_name, position, reg_number, user_id::text, signed_at`,
		tenantID, partyID, req.FullName, strings.TrimSpace(req.Position),
		req.RegNumber, req.UserID).
		Scan(&s.ID, &s.PartyID, &s.FullName, &s.Position, &s.RegNumber, &s.UserID, &s.SignedAt)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "гарын үсэг зурагч бүртгэгдсэнгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.signatory_added", docID,
		map[string]any{"party_id": partyID, "signatory_id": s.ID})
	nexus.JSON(w, http.StatusCreated, s)
}

func (m *DocumentsModule) removeSignatoryHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID, sigID := chi.URLParam(r, "id"), chi.URLParam(r, "pid"), chi.URLParam(r, "sid")

	tag, err := m.db.Exec(r.Context(),
		`DELETE FROM document_party_signatories s
		  USING document_parties p
		  WHERE s.party_id = p.id AND p.tenant_id = $1 AND p.document_id = $2
		    AND p.id = $3 AND s.id = $4 AND s.signed_at IS NULL`,
		tenantID, docID, partyID, sigID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "хасагдсангүй")
		return
	}
	if tag.RowsAffected() == 0 {
		// Зурсан хүнийг хасах нь гарын үсгийг эзэнгүй болгоно.
		nexus.Error(w, http.StatusConflict,
			"ийм бүртгэл алга, эсвэл тэр хүн аль хэдийн гарын үсэг зурсан байна")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
