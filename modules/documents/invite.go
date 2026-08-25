/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ДАНСГҮЙ ХҮНД ХҮРЭХ ХААЛГА.
//
// Гэрээний талуудын дийлэнх нь энэ суулгац дээр данстай БАЙХГҮЙ. Монгол улс
// даяар ажиллах платформ дээр «эхлээд бүртгүүлээрэй, дараа нь гэрээгээ
// уншаарай» гэж шаардах нь гэрээ байгуулахыг зогсооно. Тиймээс `person` ба
// `organisation` төрлийн тал урилгын холбоосоор ирнэ.
//
// # ХААЛГА ЯАГААД АЮУЛГҮЙ ВЭ
//
// Токен нь 32 санамсаргүй байт. Санд ЗӨВХӨН түүний SHA-256 хадгалагдана —
// сангийн хуулбар алдагдвал холбоосууд нь ажиллахгүй. Мөр бүр
// `token_sha256`-аар олдоно, өөр ямар ч замаар биш.
//
// Энэ маршрутууд нэвтрэлтгүй тул tenant нь контекстэд БАЙХГҮЙ, өөрөөр
// хэлбэл сангийн мөр түвшний хамгаалалт эдгээр асуултыг ШҮҮХГҮЙ (dbguard-ын
// платформын зам). Тиймээс ЭНЭ ФАЙЛЫН ДҮРЭМ: өгүүлбэр бүр урилгын мөрөөс
// гарсан `party_id`-гаар хязгаарлагдана, ба тэр мөр өөрөө токеноор олдоно.
// Хаягийн мөрөөс ирсэн ямар ч id шууд хэрэглэгдэхгүй.
//
// # ХОЛБООС ЮУ ХИЙЖ ЧАДАХГҮЙ ВЭ
//
// Холбоос барьсан хүн ГЭРЭЭГ УНШИНА, ТАТГАЛЗАНА, эсвэл БҮРТГЭГДСЭН хүний
// нэрээр PIN2 ёслол эхлүүлнэ. Тэр нь шинэ регистрийн дугаар зарлаж ЧАДАХГҮЙ
// — гаргагч дугаар нэрлэсэн бол. Эс бөгөөс алдагдсан холбоос нь дурын
// иргэн рүү PIN2 хүсэлт түлхэх зам болно.
package documents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// inviteTTL нь холбоосын анхдагч наслалт.
//
// Гэрээ хэдэн долоо хоног хэлэлцэгддэг тул хэт богино нь ажиллахгүй; хэт
// урт нь алдагдсан холбоосыг үүрд амьд байлгана. Гаргагч богиносгож болно.
const inviteTTL = 14 * 24 * time.Hour

// inviteMaxTTL нь гаргагч ч давж чадахгүй дээд хязгаар.
const inviteMaxTTL = 90 * 24 * time.Hour

var (
	ErrInviteUnknown = errors.New("энэ холбоос хүчингүй байна")
	ErrInviteClosed  = errors.New("энэ гэрээ хаагдсан байна")
)

// newInviteToken нь токен ба түүний хешийг буцаана. Токен нь ЭНЭ агшинд л
// байна — санд хэзээ ч биш.
func newInviteToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("токен үүсгэгдсэнгүй: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ───────────────────────────────────────────────── гаргагчийн тал: урина

// createInviteHandler нь холбоос үүсгэж НЭГ УДАА буцаана.
func (m *DocumentsModule) createInviteHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")

	var req struct {
		Channel string `json:"channel"`
		SentTo  string `json:"sent_to"`
		Days    int    `json:"days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	ttl := inviteTTL
	if req.Days > 0 {
		ttl = time.Duration(req.Days) * 24 * time.Hour
		if ttl > inviteMaxTTL {
			ttl = inviteMaxTTL
		}
	}
	channel := strings.TrimSpace(req.Channel)
	switch channel {
	case "", "link":
		channel = "link"
	case "email", "sms", "peer":
	default:
		nexus.Error(w, http.StatusBadRequest, "суваг нь link, email, sms эсвэл peer байна")
		return
	}

	// Тал нь ЭНЭ баримтынх, ЭНЭ байгууллагынх байх ёстой — ба гаргагч өөрөө
	// өөрийгөө урих нь утгагүй.
	var state, kind string
	err := m.db.QueryRow(r.Context(),
		`SELECT state, party_kind FROM document_parties
		  WHERE id = $1 AND document_id = $2 AND tenant_id = $3 AND party_role <> 'issuer'`,
		partyID, docID, tenantID).Scan(&state, &kind)
	if isNoRows(err) {
		nexus.Error(w, http.StatusNotFound, ErrPartyNotFound.Error())
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "тал уншигдсангүй")
		return
	}
	if state == PartySigned || state == PartyDeclined {
		nexus.Error(w, http.StatusConflict, ErrPartySettled.Error())
		return
	}

	token, hash, err := newInviteToken()
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	var id string
	var expires time.Time
	if err := m.db.QueryRow(r.Context(),
		`INSERT INTO document_invitations
		     (tenant_id, document_id, party_id, token_sha256, channel, sent_to, sent_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,'')::uuid, NOW() + $8::interval)
		 RETURNING id, expires_at`,
		tenantID, docID, partyID, hash, channel, strings.TrimSpace(req.SentTo),
		actorFor(r.Context()), fmt.Sprintf("%d seconds", int(ttl.Seconds()))).
		Scan(&id, &expires); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "урилга бичигдсэнгүй")
		return
	}

	if _, err := m.db.Exec(r.Context(),
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, actor_user_id, detail)
		 VALUES ($1, $2, $3, 'sent', NULLIF($4,'')::uuid, $5)`,
		tenantID, partyID, docID, actorFor(r.Context()), channel); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "урилга бичигдсэнгүй")
		return
	}

	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.invitation_created", docID,
		map[string]any{"party_id": partyID, "channel": channel})

	// Токен ЭНД, НЭГ УДАА. Дахин асуух зам байхгүй — санд хеш нь л байна.
	nexus.JSON(w, http.StatusCreated, map[string]any{
		"id": id, "token": token, "path": "/contract/" + token,
		"expires_at": expires, "channel": channel,
	})
}

// listInvitesHandler нь урилгуудыг харуулна — токенгүйгээр.
func (m *DocumentsModule) listInvitesHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	rows, err := m.db.Query(r.Context(),
		`SELECT id, channel, sent_to, sent_at, expires_at, revoked_at, opened_at, open_count
		   FROM document_invitations
		  WHERE party_id = $1 AND document_id = $2 AND tenant_id = $3
		  ORDER BY sent_at DESC`,
		chi.URLParam(r, "pid"), chi.URLParam(r, "id"), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "урилгууд уншигдсангүй")
		return
	}
	defer rows.Close()

	type invite struct {
		ID        string     `json:"id"`
		Channel   string     `json:"channel"`
		SentTo    string     `json:"sent_to,omitempty"`
		SentAt    time.Time  `json:"sent_at"`
		ExpiresAt time.Time  `json:"expires_at"`
		RevokedAt *time.Time `json:"revoked_at,omitempty"`
		OpenedAt  *time.Time `json:"opened_at,omitempty"`
		OpenCount int        `json:"open_count"`
	}
	list := []invite{}
	for rows.Next() {
		var v invite
		if err := rows.Scan(&v.ID, &v.Channel, &v.SentTo, &v.SentAt, &v.ExpiresAt,
			&v.RevokedAt, &v.OpenedAt, &v.OpenCount); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "мөр уншигдсангүй")
			return
		}
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "урилгууд бүрэн уншигдсангүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"invitations": list})
}

// revokeInviteHandler нь холбоосыг унтраана. Устгахгүй: «хэнд, хэзээ
// явуулав, хэзээ хаав» гэдэг нь маргааны үед асуугддаг зүйл.
func (m *DocumentsModule) revokeInviteHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	tag, err := m.db.Exec(r.Context(),
		`UPDATE document_invitations SET revoked_at = NOW()
		  WHERE id = $1 AND party_id = $2 AND document_id = $3 AND tenant_id = $4
		    AND revoked_at IS NULL`,
		chi.URLParam(r, "iid"), chi.URLParam(r, "pid"), chi.URLParam(r, "id"), tenantID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "холбоос хаагдсангүй")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusNotFound, "нээлттэй ийм холбоос олдсонгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.invitation_revoked",
		chi.URLParam(r, "id"), map[string]any{"invitation_id": chi.URLParam(r, "iid")})
	w.WriteHeader(http.StatusNoContent)
}

// ───────────────────────────────────────────────── токеноор ирсэн хүний тал

// invited нь токен нээсэн бүхнийг агуулна. Дараагийн өгүүлбэр бүр ЭНЭ
// бүтцээс уншина — хаягийн мөрөөс биш.
type invited struct {
	InviteID string
	PartyID  string
	DocID    string
	TenantID string
	State    string
	Title    string
	DocType  string
	Name     string
	Kind     string
}

// openInvite нь токеноор урилгыг олж, амьд эсэхийг шалгана.
//
// Хүчингүй, хугацаа дууссан, хаагдсан гурвуулаа НЭГ хариулт өгнө: аль нь
// болохыг хэлэх нь таамаглаж буй хүнд токен байсан эсэхийг мэдэгдэнэ.
func (m *DocumentsModule) openInvite(ctx context.Context, token string) (invited, error) {
	var v invited
	if len(token) < 20 || len(token) > 200 {
		return v, ErrInviteUnknown
	}
	err := m.db.QueryRow(ctx,
		`SELECT i.id, i.party_id, i.document_id, i.tenant_id::text,
		        p.state, p.doc_title, p.doc_type, p.display_name, p.party_kind
		   FROM document_invitations i
		   JOIN document_parties p ON p.id = i.party_id
		  WHERE i.token_sha256 = $1
		    AND i.revoked_at IS NULL
		    AND i.expires_at > NOW()`, hashInviteToken(token)).
		Scan(&v.InviteID, &v.PartyID, &v.DocID, &v.TenantID,
			&v.State, &v.Title, &v.DocType, &v.Name, &v.Kind)
	if isNoRows(err) {
		return v, ErrInviteUnknown
	}
	if err != nil {
		return v, fmt.Errorf("read the invitation: %w", err)
	}
	return v, nil
}

// reachInvite нь handler бүрийн эхний мөр.
func (m *DocumentsModule) reachInvite(w http.ResponseWriter, r *http.Request) (invited, bool) {
	v, err := m.openInvite(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		// 404: энэ холбоос гэж байхгүй. Хугацаа нь дуусав уу, хаагдав уу,
		// огт байгаагүй юу гэдгийг хэлэхгүй.
		nexus.Error(w, http.StatusNotFound, ErrInviteUnknown.Error())
		return v, false
	}
	return v, true
}

// inviteShowHandler нь гэрээг холбоосоор ирсэн хүнд харуулна.
func (m *DocumentsModule) inviteShowHandler(w http.ResponseWriter, r *http.Request) {
	v, ok := m.reachInvite(w, r)
	if !ok {
		return
	}

	// Нээлтийг ҮРГЭЛЖ бүртгэнэ — гэрээ хэдэн удаа, хэзээ нээгдсэн нь
	// маргааны үед асуугддаг зүйл. Талын төлөв нь харин НЭГ л удаа хөдөлнө.
	if _, err := m.db.Exec(r.Context(),
		`UPDATE document_invitations
		    SET opened_at = COALESCE(opened_at, NOW()), open_count = open_count + 1
		  WHERE id = $1`, v.InviteID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "нээлт бүртгэгдсэнгүй")
		return
	}
	if v.State == PartyInvited {
		if err := m.markViewedByInvite(r.Context(), v); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "нээлт бүртгэгдсэнгүй")
			return
		}
		v.State = PartyViewed
	}

	var bodyText, sha string
	err := m.db.QueryRow(r.Context(),
		`SELECT COALESCE(body_text,''), COALESCE(sha256,'')
		   FROM document_party_files WHERE party_id = $1`, v.PartyID).Scan(&bodyText, &sha)
	if err != nil && !isNoRows(err) {
		nexus.Error(w, http.StatusInternalServerError, "хувь уншигдсангүй")
		return
	}

	parties, err := m.partyBriefs(r.Context(), v)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "талууд уншигдсангүй")
		return
	}
	signatories, err := m.inviteSignatories(r.Context(), v.PartyID)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гарын үсэг зурагчид уншигдсангүй")
		return
	}

	// Гарын үсэг зурагчийг өөрсдөө нэрлэх боломжтой эсэх. Гаргагч дугаар
	// нэрлэсэн бол ҮГҮЙ: эс бөгөөс алдагдсан холбоос нь дурын иргэн рүү
	// PIN2 хүсэлт түлхэх зам болно.
	mayNominate := true
	for _, s := range signatories {
		if strings.TrimSpace(s.RegNumber) != "" {
			mayNominate = false
			break
		}
	}

	nexus.JSON(w, http.StatusOK, map[string]any{
		"party_id": v.PartyID, "display_name": v.Name, "party_kind": v.Kind,
		"title": v.Title, "doc_type": v.DocType, "state": v.State,
		"body_text": bodyText, "sha256": sha, "has_copy": sha != "",
		"parties": parties, "my_signatories": signatories,
		"may_nominate": mayNominate,
	})
}

func (m *DocumentsModule) markViewedByInvite(ctx context.Context, v invited) error {
	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE document_parties
		    SET state = 'viewed', viewed_at = COALESCE(viewed_at, NOW()), updated_at = NOW()
		  WHERE id = $1 AND state = 'invited'`, v.PartyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO document_party_events
		     (tenant_id, party_id, document_id, kind, actor_label, detail)
		 VALUES ($1, $2, $3, 'viewed', 'урилгын холбоос', '')`,
		v.TenantID, v.PartyID, v.DocID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *DocumentsModule) partyBriefs(ctx context.Context, v invited) ([]inboxBrief, error) {
	rows, err := m.db.Query(ctx,
		`SELECT id, party_role, display_name, state FROM document_parties
		  WHERE document_id = $1 AND tenant_id = $2 ORDER BY ordinal`, v.DocID, v.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []inboxBrief{}
	for rows.Next() {
		var id string
		var b inboxBrief
		if err := rows.Scan(&id, &b.Role, &b.Name, &b.State); err != nil {
			return nil, err
		}
		b.Mine = id == v.PartyID
		out = append(out, b)
	}
	return out, rows.Err()
}

func (m *DocumentsModule) inviteSignatories(ctx context.Context, partyID string) ([]Signatory, error) {
	rows, err := m.db.Query(ctx,
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

func (m *DocumentsModule) inviteCopyHandler(w http.ResponseWriter, r *http.Request) {
	m.serveInviteCopy(w, r, "content", errNotSent.Error())
}

func (m *DocumentsModule) inviteSignedCopyHandler(w http.ResponseWriter, r *http.Request) {
	m.serveInviteCopy(w, r, "signed_content", "гарын үсэгтэй хувь хараахан алга")
}

// serveInviteCopy — баганын нэр нь ЭНД тогтмол, хүсэлтээс ХЭЗЭЭ Ч ирэхгүй.
func (m *DocumentsModule) serveInviteCopy(w http.ResponseWriter, r *http.Request, column, missing string) {
	v, ok := m.reachInvite(w, r)
	if !ok {
		return
	}
	var name string
	var pdf []byte
	err := m.db.QueryRow(r.Context(),
		`SELECT file_name, `+column+` FROM document_party_files WHERE party_id = $1`,
		v.PartyID).Scan(&name, &pdf)
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

// inviteNominateHandler нь дансгүй БАЙГУУЛЛАГА өөрийн гарын үсэг зурагчаа
// зарлана.
//
// ГАНЦХАН УДАА, ба зөвхөн гаргагч дугаар нэрлээгүй үед. Тэр хоёр хязгаар нь
// холбоосыг PIN2 түлхэх хэрэгсэл болгохоос сэргийлнэ: холбоос алдагдсан ч
// хүсэлт нь аль хэдийн нэрлэгдсэн иргэн рүү л очно.
func (m *DocumentsModule) inviteNominateHandler(w http.ResponseWriter, r *http.Request) {
	v, ok := m.reachInvite(w, r)
	if !ok {
		return
	}
	if v.State != PartyInvited && v.State != PartyViewed {
		nexus.Error(w, http.StatusConflict, ErrPartySettled.Error())
		return
	}

	var req struct {
		FullName  string `json:"full_name"`
		Position  string `json:"position"`
		RegNumber string `json:"reg_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "хүсэлт уншигдсангүй")
		return
	}
	req.FullName = strings.TrimSpace(req.FullName)
	req.RegNumber = strings.ToUpper(strings.TrimSpace(req.RegNumber))
	if req.FullName == "" || req.RegNumber == "" {
		nexus.Error(w, http.StatusBadRequest, "овог нэр ба регистрийн дугаарыг бичнэ үү")
		return
	}

	// Аль хэдийн дугаартай мөр байвал ТАТГАЛЗАНА. Санд шалгуулж, уралдааныг
	// ч хаана: нөхцөлт INSERT нь хоёр зэрэгцээ хүсэлтийн нэгийг л оруулна.
	var id string
	err := m.db.QueryRow(r.Context(),
		`INSERT INTO document_party_signatories
		     (tenant_id, party_id, document_id, full_name, position, reg_number,
		      reg_number_declared_at)
		 SELECT $1, $2, $3, $4, $5, $6, NOW()
		  WHERE NOT EXISTS (SELECT 1 FROM document_party_signatories g
		                     WHERE g.party_id = $2 AND btrim(g.reg_number) <> '')
		 RETURNING id`,
		v.TenantID, v.PartyID, v.DocID, req.FullName, strings.TrimSpace(req.Position),
		req.RegNumber).Scan(&id)
	if isNoRows(err) {
		nexus.Error(w, http.StatusConflict,
			"энэ талд гарын үсэг зурах хүн аль хэдийн нэрлэгдсэн байна")
		return
	}
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "гарын үсэг зурагч бүртгэгдсэнгүй")
		return
	}

	if _, err := m.db.Exec(r.Context(),
		`INSERT INTO document_party_events
		     (tenant_id, party_id, document_id, kind, actor_label, detail)
		 VALUES ($1, $2, $3, 'nominated', 'урилгын холбоос', $4)`,
		v.TenantID, v.PartyID, v.DocID, req.FullName); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "бүртгэл бичигдсэнгүй")
		return
	}
	nexus.JSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (m *DocumentsModule) inviteSignStartHandler(w http.ResponseWriter, r *http.Request) {
	v, ok := m.reachInvite(w, r)
	if !ok {
		return
	}
	// Токеноор ирсэн хүн нэвтрээгүй тул `userID` хоосон: `signerFor` нь
	// талын цорын ганц бүртгэгдсэн хүнийг авах ба хэд хэдэн бол татгалзана.
	s, err := m.signerFor(r.Context(), v.TenantID, v.DocID, v.PartyID, "")
	switch {
	case errors.Is(err, ErrNoSignatory), errors.Is(err, ErrNoPartyCopy),
		errors.Is(err, ErrNotYourTurn):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	session, err := m.startPartySignature(r.Context(), s, "")
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

	if _, err := m.db.Exec(r.Context(),
		`INSERT INTO document_party_events
		     (tenant_id, party_id, document_id, kind, actor_label, detail)
		 VALUES ($1, $2, $3, 'ceremony_started', 'урилгын холбоос', $4)`,
		v.TenantID, v.PartyID, v.DocID, session.SessionID); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "бүртгэл бичигдсэнгүй")
		return
	}
	nexus.JSON(w, http.StatusOK, session)
}

func (m *DocumentsModule) inviteSignPollHandler(w http.ResponseWriter, r *http.Request) {
	v, ok := m.reachInvite(w, r)
	if !ok {
		return
	}
	progress, err := m.pollPartySignature(r.Context(), v.TenantID, v.DocID, v.PartyID)
	switch {
	case errors.Is(err, ErrBytesChanged), errors.Is(err, ErrPartySettled):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrNoSigningRail):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, ErrPartyNotFound):
		nexus.Error(w, http.StatusNotFound, ErrInviteUnknown.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	nexus.JSON(w, http.StatusOK, progress)
}

func (m *DocumentsModule) inviteDeclineHandler(w http.ResponseWriter, r *http.Request) {
	v, ok := m.reachInvite(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
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
		  WHERE id = $1 AND state IN ('invited','viewed')`, v.PartyID, reason)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusConflict, ErrPartySettled.Error())
		return
	}
	if _, err := tx.Exec(r.Context(),
		`INSERT INTO document_party_events
		     (tenant_id, party_id, document_id, kind, actor_label, detail)
		 VALUES ($1, $2, $3, 'declined', 'урилгын холбоос', $4)`,
		v.TenantID, v.PartyID, v.DocID, reason); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"state": PartyDeclined})
}
