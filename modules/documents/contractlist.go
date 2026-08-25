/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ГЭРЭЭНИЙ ЖАГСААЛТ — баримтын жагсаалтаас ТУСДАА.
//
// `GET /api/v1/documents/` нь БАРИМТЫН жагсаалт: гарчиг, төрөл, зөвшөөрлийн
// дараалалд хэдэн гарын үсэг цуглав. Гэрээний жагсаалт өөр асуултад хариулна
// — хэнтэй, ямар төлөвт, хэдэн тал зурав, хэзээ дуусах вэ.
//
// Тэдгээрийг нэг хариултад нийлүүлж болох ч болохгүй: `Document` бүтцийг өөр
// репо дахь клиентүүд уншдаг бөгөөд түүнд гэрээний найман талбар нэмэх нь
// гэрээ огт хэрэглэдэггүй хүн бүрд тэдгээрийг ачаална. Хоёр дэлгэц, хоёр
// асуулт, хоёр хариулт.
package documents

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// ContractRow нь гэрээний жагсаалтын нэг мөр.
type ContractRow struct {
	ID             string     `json:"id"`
	Number         string     `json:"contract_number,omitempty"`
	Title          string     `json:"title"`
	DocType        string     `json:"doc_type"`
	State          string     `json:"contract_state"`
	Mode           string     `json:"signing_mode"`
	Counterparties string     `json:"counterparties"`
	PartyCount     int        `json:"party_count"`
	SignedCount    int        `json:"signed_count"`
	RequiredCount  int        `json:"required_count"`
	DeclinedCount  int        `json:"declined_count"`
	Amount         *float64   `json:"amount,omitempty"`
	Currency       string     `json:"currency,omitempty"`
	EffectiveFrom  *time.Time `json:"effective_from,omitempty"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	ExecutedAt     *time.Time `json:"executed_at,omitempty"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	EffectiveTo    *time.Time `json:"effective_to,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// contractListLimit нь нэг хуудасны дээд хэмжээ.
const contractListLimit = 200

// listContractsHandler нь гэрээ бүрийг НЭГ мөрөөр буцаана.
//
// Талуудыг нэгтгэх нь query дотор: гэрээ бүрт нэмэлт дуудлага хийх дэлгэц нь
// зуун гэрээтэй байгууллагад зуун дуудлага хийнэ, ба тэдгээрийн нэг нь
// амжилтгүй болоход жагсаалт нь хэсэгчилсэн үнэн харуулна.
func (m *DocumentsModule) listContractsHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}

	state := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("state")))
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := contractListLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			nexus.Error(w, http.StatusBadRequest, "limit нь эерэг тоо байна")
			return
		}
		if parsed < limit {
			limit = parsed
		}
	}

	rows, err := m.db.Query(r.Context(),
		`SELECT r.id, r.contract_number, r.title, r.doc_type, r.contract_state, r.signing_mode,
		        COALESCE(string_agg(DISTINCT c.display_name, ', ')
		                 FILTER (WHERE c.party_role <> 'issuer'), ''),
		        count(*) FILTER (WHERE c.party_role <> 'issuer'),
		        count(*) FILTER (WHERE c.party_role <> 'issuer' AND c.required AND c.state = 'signed'),
		        count(*) FILTER (WHERE c.party_role <> 'issuer' AND c.required),
		        count(*) FILTER (WHERE c.party_role <> 'issuer' AND c.state = 'declined'),
		        r.amount, COALESCE(r.currency, ''),
		        r.effective_from, r.sent_at, r.executed_at, r.due_at, r.effective_to, r.created_at
		   FROM document_records r
		   LEFT JOIN document_parties c ON c.document_id = r.id AND c.tenant_id = r.tenant_id
		  WHERE r.tenant_id = $1
		    AND r.contract_state <> 'NONE'
		    AND ($2 = '' OR r.contract_state = $2)
		    AND ($3 = '' OR r.title ILIKE '%' || $3 || '%'
		                 OR r.contract_number ILIKE '%' || $3 || '%')
		  GROUP BY r.id
		  ORDER BY r.created_at DESC
		  LIMIT $4`, tenantID, state, search, limit)
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гэрээнүүд уншигдсангүй")
		return
	}
	defer rows.Close()

	list := []ContractRow{}
	for rows.Next() {
		var v ContractRow
		if err := rows.Scan(&v.ID, &v.Number, &v.Title, &v.DocType, &v.State, &v.Mode,
			&v.Counterparties, &v.PartyCount, &v.SignedCount, &v.RequiredCount, &v.DeclinedCount,
			&v.Amount, &v.Currency,
			&v.EffectiveFrom, &v.SentAt, &v.ExecutedAt, &v.DueAt, &v.EffectiveTo,
			&v.CreatedAt); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "мөр уншигдсангүй")
			return
		}
		list = append(list, v)
	}
	if err := rows.Err(); err != nil {
		nexus.Error(w, http.StatusInternalServerError, "гэрээнүүд бүрэн уншигдсангүй")
		return
	}
	nexus.JSON(w, http.StatusOK, map[string]any{"contracts": list})
}

// saveContractFactsHandler нь гэрээний ХЭРЭГ ФАКТУУДЫГ бичнэ: дугаар, дүн,
// хүчинтэй хугацаа, эцсийн хугацаа.
//
// Эдгээр нь `document_records`-д амьдардаг ч `createDocumentHandler` нь
// зөвхөн гарчиг, төрөл авдаг: тэр нь БАРИМТ үүсгэдэг, гэрээ биш. Гэрээний
// талбарууд нь тал нэмэгдсэний дараа л утгатай.
func (m *DocumentsModule) saveContractFactsHandler(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := nexus.RequireTenant(w, r)
	if !ok {
		return
	}
	docID := chi.URLParam(r, "id")

	var req struct {
		Number        string   `json:"contract_number"`
		Amount        *float64 `json:"amount"`
		Currency      string   `json:"currency"`
		EffectiveFrom string   `json:"effective_from"`
		EffectiveTo   string   `json:"effective_to"`
		DueAt         string   `json:"due_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		nexus.Error(w, http.StatusBadRequest, "хүсэлт уншигдсангүй")
		return
	}
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if req.Amount != nil && req.Currency == "" {
		// Санд ч ижил хязгаарлалт байгаа — энэ нь зөвхөн уншигдахуйц алдаа.
		nexus.Error(w, http.StatusBadRequest, "дүн бичсэн бол валютаа бичнэ үү")
		return
	}
	if len(req.Currency) > 3 {
		nexus.Error(w, http.StatusBadRequest, "валют нь гурван үсэг байна (MNT, USD…)")
		return
	}

	tag, err := m.db.Exec(r.Context(),
		`UPDATE document_records
		    SET contract_number = $3,
		        amount   = $4,
		        currency = NULLIF($5, ''),
		        effective_from = NULLIF($6, '')::date,
		        effective_to   = NULLIF($7, '')::date,
		        due_at         = NULLIF($8, '')::timestamptz
		  WHERE id = $1 AND tenant_id = $2`,
		docID, tenantID, strings.TrimSpace(req.Number), req.Amount, req.Currency,
		strings.TrimSpace(req.EffectiveFrom), strings.TrimSpace(req.EffectiveTo),
		strings.TrimSpace(req.DueAt))
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "гэрээний мэдээлэл хадгалагдсангүй")
		return
	}
	if tag.RowsAffected() == 0 {
		nexus.Error(w, http.StatusNotFound, "гэрээ олдсонгүй")
		return
	}
	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.contract_facts_saved", docID, nil)
	w.WriteHeader(http.StatusNoContent)
}
