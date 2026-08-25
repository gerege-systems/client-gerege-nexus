/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// Талын өмнөөс гарын үсэг зурах ёслол.
//
// ЭНЭ ФАЙЛЫН ГОЛ ДҮРЭМ: ЁСЛОЛ ХЭНД ХАНДАХЫГ ХҮСЭЛТИЙН БИЕ ХЭЛЭХГҮЙ.
//
// Модулийн хуучин `POST /{id}/sign/eid/start` нь регистрийн дугаарыг хүсэлтээс
// авдаг: дуудагч ямар ч дугаар бичиж, ямар ч иргэний утас руу PIN2 хүсэлт
// илгээж чадна. Дараалалгүй баримт дээр тэр нь хамгаалалтгүй үлддэг.
//
// Талтай гэрээнд дугаар нь `document_party_signatories` мөрөөс гарна — тэр
// мөрийг зөвхөн `documents.parties` эрхтэй хүн (эсвэл хүлээн авагч тенант
// өөрөө) бичдэг. Өөрөөр хэлбэл «хэн гарын үсэг зурж болох вэ» гэдэг нь
// бүртгэлийн асуулт, хүсэлтийн талбарын биш.
//
// ХОЁР ДАХЬ ДҮРЭМ: НЭГ ТАЛ ДЭЭР НЭГ Л НЭЭЛТТЭЙ ЁСЛОЛ.
//
// Хоёр цонхноос зэрэг эхлүүлсэн ёслол нэг нэгнийхээ дугаарыг дарж бичвэл
// эхнийх нь ЖИНХЭНЭ PIN2 зөвшөөрөл өнчирч, хоёр дахийн poll түүнийг
// өөрийнхөөрөө бичих ба гарын үсэг зурсан хүн бүртгэгдсэнээс өөр хүн болно.
//
// ГУРАВ ДАХЬ ДҮРЭМ: ЁСЛОЛЫН ЯВЦАД БАЙТ СОЛИГДВОЛ ГАРЫН ҮСЭГ ХҮЛЭЭН
// АВАГДАХГҮЙ. Дахин хүргэлт хөлдсөн хувийг сольсон бол зурагдсан гарын үсэг
// өөр бичвэрийг хамарна; хоёрыг нэг мөрд хадгалах нь худал бүртгэл.
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

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
)

// partySigner нь ёслол хэнд хандахыг шийдсэн үр дүн.
type partySigner struct {
	PartyID     string
	SignatoryID string
	FullName    string
	RegNumber   string
	DocID       string
	TenantID    string
	PDF         []byte
	SHA256      string
	FileName    string
	Title       string
}

var (
	ErrNotYourTurn   = errors.New("энэ гэрээ дараалалтай — өмнөх тал гарын үсгээ зураагүй байна")
	ErrNoPartyCopy   = errors.New("энэ талд хөлдсөн хувь алга — гэрээ хараахан хүргэгдээгүй")
	ErrBytesChanged  = errors.New("гэрээний бичвэр ёслолын явцад шинэчлэгдсэн байна — дахин уншиж, дахин зурна уу")
	ErrNoSigningRail = errors.New("энэ суулгацад гарын үсгийн рельс алга")
)

// signerFor нь тухайн талын өмнөөс ХЭН зурахыг бүртгэлээс шийднэ.
//
// `userID` хоосон биш бол (нэвтэрсэн хүн) түүний өөрийн мөрийг эрэлхийлнэ;
// олдохгүй бол тухайн талын цорын ганц гарын үсэг зурагчийг авна. Хоёроос
// олон бүртгэлтэй, аль нь ч дуудагчид хамаарахгүй бол татгалзана: аль нэгийг
// нь сонгох нь гарын үсгийг буруу хүний нэр дээр бүртгэх зам.
func (m *DocumentsModule) signerFor(ctx context.Context, tenantID, docID, partyID, userID string) (partySigner, error) {
	var s partySigner
	s.TenantID, s.DocID, s.PartyID = tenantID, docID, partyID

	// ДАРААЛАЛ. `joint` горимд тал бүр `sign_order` авдаг ба өмнөх нь
	// зураагүй байхад дараагийнх нь зурах ёсгүй — «эхлээд захирал, дараа нь
	// нягтлан» гэдэг нь зөвхөн дэлгэцийн дараалал биш, гэрээний нөхцөл.
	//
	// Шалгалт нь ЭНД байгаа нь санаатай: гурван зам (гаргагч, хүлээн авагч,
	// урилгын токен) гурвуулаа энэ функцээр ёслолоо эхлүүлдэг тул дараалал
	// нэг л газарт бичигдэнэ. Гурван газарт бичигдсэн дүрэм нь хоёр дахь
	// газраа мартагдах дүрэм.
	//
	// Горимыг `document_records`-оос асуухгүй: хүлээн авагч тэр хүснэгтийг
	// уншиж чаддаггүй. `sign_order` нь зөвхөн `joint` горимд бичигддэг тул
	// баганын оршихуй нь өөрөө дараалалтай гэдгийн дохио.
	var blockedBy string
	err := m.db.QueryRow(ctx,
		`SELECT COALESCE(string_agg(earlier.display_name, ', ' ORDER BY earlier.sign_order), '')
		   FROM document_parties me
		   JOIN document_parties earlier
		     ON earlier.document_id = me.document_id
		    AND earlier.party_role <> 'issuer'
		    AND earlier.required
		    AND earlier.sign_order IS NOT NULL
		    AND earlier.sign_order < me.sign_order
		    AND earlier.state <> 'signed'
		  WHERE me.id = $1 AND me.document_id = $2 AND me.sign_order IS NOT NULL`,
		partyID, docID).Scan(&blockedBy)
	if err != nil && !isNoRows(err) {
		return s, fmt.Errorf("read the signing order: %w", err)
	}
	if blockedBy != "" {
		return s, fmt.Errorf("%w: %s", ErrNotYourTurn, blockedBy)
	}

	rows, err := m.db.Query(ctx,
		`SELECT g.id, g.full_name, g.reg_number, COALESCE(g.user_id::text, '')
		   FROM document_party_signatories g
		   JOIN document_parties p ON p.id = g.party_id
		  WHERE p.id = $1 AND p.document_id = $2 AND g.signed_at IS NULL
		  ORDER BY g.created_at`, partyID, docID)
	if err != nil {
		return s, fmt.Errorf("read signatories: %w", err)
	}
	defer rows.Close()

	type row struct{ id, name, reg, user string }
	var all []row
	for rows.Next() {
		var v row
		if err := rows.Scan(&v.id, &v.name, &v.reg, &v.user); err != nil {
			return s, err
		}
		all = append(all, v)
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	if len(all) == 0 {
		return s, ErrNoSignatory
	}

	pick := all[0]
	if userID != "" {
		matched := false
		for _, v := range all {
			if v.user == userID {
				pick, matched = v, true
				break
			}
		}
		// Нэвтэрсэн хүн энэ талын бүртгэлд байхгүй ба сонгох боломж олон
		// байвал таамаглахгүй.
		if !matched && len(all) > 1 {
			return s, errors.New("энэ талд хэд хэдэн гарын үсэг зурагч бүртгэлтэй — та тэдний нэг нь биш байна")
		}
	}
	if pick.reg == "" {
		return s, errors.New("гарын үсэг зурагчийн регистрийн дугаар бүртгэгдээгүй байна")
	}
	s.SignatoryID, s.FullName, s.RegNumber = pick.id, pick.name, pick.reg

	// Зурагдах байт нь ХӨЛДСӨН хувь. Дахин үүсгэхгүй.
	//
	// Гарчиг нь ТАЛЫН мөрөөс — `document_records`-оос БИШ. Хоёр шалтгаан:
	// хүлээн авагчийн сесс тэр хүснэгтийг уншиж чаддаггүй (уншиж ч болохгүй),
	// ба PIN2 дэлгэц дээр гарах бичвэр нь тэдний ХАРСАН гарчиг байх ёстой —
	// гаргагч дараа нь гарчгаа заcсан ч.
	err = m.db.QueryRow(ctx,
		`SELECT f.file_name, f.sha256, f.content, p.doc_title
		   FROM document_party_files f
		   JOIN document_parties p ON p.id = f.party_id
		  WHERE f.party_id = $1 AND f.tenant_id = $2`, partyID, tenantID).
		Scan(&s.FileName, &s.SHA256, &s.PDF, &s.Title)
	if isNoRows(err) || len(s.PDF) == 0 {
		return s, ErrNoPartyCopy
	}
	if err != nil {
		return s, fmt.Errorf("read the frozen copy: %w", err)
	}
	return s, nil
}

// startPartySignature нь PIN2 ёслолыг эхлүүлж, талын мөрөнд түгжинэ.
func (m *DocumentsModule) startPartySignature(ctx context.Context, s partySigner, actor string) (*EIDSignSession, error) {
	if m.signer == nil || !m.signer.Enabled() {
		return nil, ErrNoSigningRail
	}

	started, err := m.signer.SignDocument(ctx, nexus.DocumentSignatureRequest{
		RegNumber:   s.RegNumber,
		FullName:    s.FullName,
		FileName:    s.FileName,
		PDF:         s.PDF,
		DisplayText: signatureDisplayText(s.Title),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: гарын үсгийн рельс иргэнд хүрч чадсангүй: %w",
			ErrProviderUnavailable, err)
	}

	// Ёслолыг талын мөрөнд түгжинэ. Нээлттэй ёслол байхад хоёр дахийг
	// зөвшөөрөхгүй; платформын өөрийн 20 минутын хугацаа өнгөрсний дараа л
	// шинийг зөвшөөрнө, эс бөгөөс орхигдсон ёслол талыг үүрд түгжинэ.
	tag, err := m.db.Exec(ctx,
		`UPDATE document_parties
		    SET session_id = $3, session_at = NOW(), session_by = NULLIF($4,'')::uuid,
		        session_signatory_id = $5, updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2
		    AND state IN ('invited','viewed')
		    AND (session_id IS NULL OR session_at IS NULL OR session_at < NOW() - INTERVAL '20 minutes')`,
		s.PartyID, s.TenantID, started.SessionID, actor, s.SignatoryID)
	if err != nil {
		return nil, fmt.Errorf("record the ceremony: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrCeremonyOpen
	}

	// Ёслол ба хөлдсөн байтыг хослуулж бичнэ: буцаж ирсэн зүйлийг илгээсэн
	// зүйлтэй тулгах цорын ганц зам.
	// `party_id` нь бүртгэл биш, ХААЛГА: түүнээс trigger нь мөрийн
	// `counterparty_tenant_id`-г гаргах ба тэр нь хүлээн авагчийн сессэд
	// энэ мөрийг бичих цорын ганц зөвшөөрөл (00005).
	if _, err := m.db.Exec(ctx,
		`INSERT INTO document_eid_sign_sessions
		     (session_id, tenant_id, document_id, reg_number, display_text, requested_digest, format, party_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		started.SessionID, s.TenantID, s.DocID, s.RegNumber,
		signatureDisplayText(s.Title), s.SHA256, string(domain.FormatPAdES), s.PartyID); err != nil {
		return nil, fmt.Errorf("record signing session: %w", err)
	}

	return &EIDSignSession{
		SessionID:        started.SessionID,
		VerificationCode: started.VerificationCode,
		DisplayText:      signatureDisplayText(s.Title),
	}, nil
}

// pollPartySignature нь ёслолыг дуусгаж, гарын үсгийг бүртгэнэ.
func (m *DocumentsModule) pollPartySignature(ctx context.Context, tenantID, docID, partyID string) (*EIDSignProgress, error) {
	if m.signer == nil || !m.signer.Enabled() {
		return nil, ErrNoSigningRail
	}

	// Регистрийн дугаар нь ЁСЛОЛ ЭХЛҮҮЛСЭН мөрөөс гарна.
	//
	// Урьд нь энэ query «гарын үсэг зураагүй хамгийн эртний бүртгэлтэй хүн»
	// гэж таамагладаг байв. Хоёр гарын үсэг зурагчтай тал дээр хоёр дахь нь
	// ёслол эхлүүлбэл poll нь ЭХНИЙХИЙН дугаараар рельсээс асуух ба буцаж
	// ирсэн гарын үсгийг тэр хүний нэр дээр бүртгэнэ — өөр иргэний PIN2-ыг
	// өөр хүний нэр дээр бичих зам.
	var sessionID, regNumber, signatoryID, frozenSHA string
	var pdf []byte
	err := m.db.QueryRow(ctx,
		`SELECT COALESCE(p.session_id,''), COALESCE(g.reg_number,''), COALESCE(g.id::text,''),
		        f.sha256, f.content
		   FROM document_parties p
		   JOIN document_party_files f ON f.party_id = p.id
		   LEFT JOIN document_party_signatories g ON g.id = p.session_signatory_id
		  WHERE p.id = $1 AND p.tenant_id = $2 AND p.document_id = $3`,
		partyID, tenantID, docID).Scan(&sessionID, &regNumber, &signatoryID, &frozenSHA, &pdf)
	if isNoRows(err) {
		return nil, ErrPartyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read the ceremony: %w", err)
	}
	if sessionID == "" {
		return nil, errors.New("гарын үсгийн ёслол эхлээгүй байна")
	}
	if signatoryID == "" || regNumber == "" {
		// Ёслол нээлттэй мөртлөө хэнийх нь мэдэгдэхгүй бол таамаглахгүй.
		return nil, errors.New("энэ ёслолыг эхлүүлсэн гарын үсэг зурагч бүртгэлээс алга")
	}

	state, err := m.signer.PollSignature(ctx, regNumber, sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: гарын үсгийн рельс хариулсангүй: %w", ErrProviderUnavailable, err)
	}
	if state != nexus.SignatureCompleted {
		if state.Settled() {
			// Дууссан ч гарын үсэг гараагүй бол түгжээг тавьж үлдээхгүй.
			_, _ = m.db.Exec(ctx,
				`UPDATE document_parties
				    SET session_id = NULL, session_at = NULL, session_signatory_id = NULL
				  WHERE id = $1 AND tenant_id = $2 AND session_id = $3`, partyID, tenantID, sessionID)
		}
		return &EIDSignProgress{State: approvalStateOf(state)}, nil
	}

	signed, err := m.signer.SignedDocument(ctx, regNumber, sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: гарын үсэгтэй баримт татагдсангүй: %w", ErrProviderUnavailable, err)
	}
	if len(signed.PDF) == 0 {
		return nil, fmt.Errorf("%w: рельс гарын үсэг зурлаа гэж хэлээд баримт буцаасангүй",
			ErrProviderUnavailable)
	}

	// Ёслол эхэлснээс хойш хөлдсөн байт солигдсон эсэхийг ЭНД шалгана.
	if domain.Digest(pdf) != frozenSHA {
		return nil, ErrBytesChanged
	}

	if err := m.recordPartySignature(ctx, tenantID, docID, partyID, sessionID,
		signatoryID, regNumber, signed.PDF, frozenSHA); err != nil {
		return nil, err
	}
	return &EIDSignProgress{State: ApprovalComplete}, nil
}

// recordPartySignature нь гарын үсгийг НЭГ гүйлгээнд бичнэ.
//
// Гурван зүйл хамт: талын төлөв, гарын үсэгтэй байт, гарын үсгийн бүртгэл.
// Аль нэг нь бичигдээгүй бол бусад нь ч бичигдэхгүй — гарын үсэгтэй байт
// байгаа мөртлөө бүртгэлгүй тал бол хариултгүй асуулт.
func (m *DocumentsModule) recordPartySignature(ctx context.Context, tenantID, docID, partyID,
	sessionID, signatoryID, regNumber string, signedPDF []byte, coveredDigest string) error {

	// Иргэн аль хэдийн зөвшөөрсөн тул дуудагч холболтоо тасалсан ч гарын
	// үсэг устах ёсгүй.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Зөвхөн нээлттэй ёслолтой, шийдвэрлэгдээгүй тал. Хоёр poll зэрэг ирвэл
	// нэг нь л дийлнэ.
	var settled, fullName string
	err = tx.QueryRow(ctx,
		`UPDATE document_parties
		    SET state = 'signed', signed_at = NOW(), session_id = NULL, session_at = NULL,
		        session_signatory_id = NULL, updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2 AND session_id = $3 AND state IN ('invited','viewed')
		 RETURNING id`, partyID, tenantID, sessionID).Scan(&settled)
	if isNoRows(err) {
		return ErrPartySettled
	}
	if err != nil {
		return fmt.Errorf("settle the party: %w", err)
	}

	// Яг ТЭР мөр — `id`-гаар, дугаараар биш. Нэг иргэн нэг гэрээнд хоёр
	// талын өмнөөс бүртгэгдсэн байж болно (жишээ нь өөрийн компани ба
	// өөрийн нэрээр), тэгэхэд дугаараар хайх нь буруу мөрийг хаана.
	if err = tx.QueryRow(ctx,
		`UPDATE document_party_signatories
		    SET signed_at = NOW()
		  WHERE id = $1 AND party_id = $2 AND reg_number = $3 AND signed_at IS NULL
		 RETURNING full_name`, signatoryID, partyID, regNumber).Scan(&fullName); err != nil {
		if isNoRows(err) {
			return errors.New("гарын үсэг зурсан хүн энэ талын бүртгэлд алга")
		}
		return fmt.Errorf("settle the signatory: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`UPDATE document_party_files SET signed_content = $3, signed_at = NOW()
		  WHERE party_id = $1 AND tenant_id = $2`, partyID, tenantID, signedPDF); err != nil {
		return fmt.Errorf("store the signed copy: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`INSERT INTO document_signatures
		     (tenant_id, document_id, signer_name, signer_reg_number, signer_method,
		      signature_hash, format, covered_digest, party_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		tenantID, docID, fullName+" (E-ID баталгаажсан)", regNumber, SignerEID,
		"eid_session_"+sessionID, string(domain.FormatPAdES), coveredDigest, partyID); err != nil {
		return fmt.Errorf("record the signature: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`UPDATE document_eid_sign_sessions SET consumed_at = NOW()
		  WHERE session_id = $1 AND tenant_id = $2`, sessionID, tenantID); err != nil {
		return fmt.Errorf("consume the session: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`INSERT INTO document_party_events (tenant_id, party_id, document_id, kind, detail)
		 VALUES ($1, $2, $3, 'signed', $4)`, tenantID, partyID, docID, regNumber); err != nil {
		return fmt.Errorf("record the event: %w", err)
	}

	// `document_records.contract_state`-ыг ЭНД шинэчлэхгүй: 00005-ийн trigger
	// нь талын мөр өөрчлөгдөх бүрд тоолно. Гурван зам (гаргагч, хүлээн
	// авагч, урилгын токен) нэг тоолуур хуваах нь хоёр дахь замдаа
	// мартагдахгүйн цорын ганц баталгаа — ба хүлээн авагчийн сесс
	// `document_records` руу бичих эрхгүй.
	return tx.Commit(ctx)
}

// ─────────────────────────────────────────────────────────────── handler-ууд

func (m *DocumentsModule) partySignStartHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")

	claims, _ := nexus.UserFromContext(r.Context())
	s, err := m.signerFor(r.Context(), tenantID, docID, partyID, claims.UserID)
	switch {
	case errors.Is(err, ErrNoSignatory), errors.Is(err, ErrNoPartyCopy),
		errors.Is(err, ErrNotYourTurn):
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
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.party_sign_started", docID,
		map[string]any{"party_id": partyID, "session_id": session.SessionID})
	nexus.JSON(w, http.StatusOK, session)
}

func (m *DocumentsModule) partySignPollHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")

	progress, err := m.pollPartySignature(r.Context(), tenantID, docID, partyID)
	switch {
	case errors.Is(err, ErrBytesChanged), errors.Is(err, ErrPartySettled):
		nexus.Error(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, ErrNoSigningRail):
		nexus.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		nexus.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if progress.State == ApprovalComplete {
		nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.party_signed", docID,
			map[string]any{"party_id": partyID})
	}
	nexus.JSON(w, http.StatusOK, progress)
}

// declinePartyHandler — татгалзал нь eID-ийн үйлдэл биш, бизнесийн шийдвэр.
func (m *DocumentsModule) declinePartyHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID, partyID := chi.URLParam(r, "id"), chi.URLParam(r, "pid")

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Reason) == "" {
		// Шалтгаангүй татгалзал нь гаргагчид «юу засах вэ» гэдгийг хэлдэггүй.
		nexus.Error(w, http.StatusBadRequest, "татгалзсан шалтгаанаа бичнэ үү")
		return
	}

	tx, err := m.db.Begin(r.Context())
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	tag, err := tx.Exec(r.Context(),
		`UPDATE document_parties
		    SET state = 'declined', declined_at = NOW(), decline_reason = $3,
		        session_id = NULL, session_at = NULL, session_signatory_id = NULL, updated_at = NOW()
		  WHERE id = $1 AND tenant_id = $2 AND state IN ('invited','viewed')`,
		partyID, tenantID, strings.TrimSpace(req.Reason))
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
		 VALUES ($1, $2, $3, 'declined', $4)`, tenantID, partyID, docID, strings.TrimSpace(req.Reason)); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "татгалзал бичигдсэнгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.party_declined", docID,
		map[string]any{"party_id": partyID})
	nexus.JSON(w, http.StatusOK, map[string]any{"state": PartyDeclined})
}
