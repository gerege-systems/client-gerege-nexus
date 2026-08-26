/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// ОЛОН ХҮЛЭЭН АВАГЧИЙГ НЭГ ФАЙЛААР.
//
// 800 сургуультай гэрээ байгуулах хүн 800 маягт бөглөхгүй — жагсаалтаа Excel
// дээр хөтөлж явдаг, тэр файлаа л өгнө. Мөр бүр нэг хүлээн авагч:
//
//	1-р багана  Байгууллага/хүний нэр        (заавал)
//	2-р багана  Байгууллагын регистр          (заавал биш)
//	3-р багана  Гарын үсэг зурагчийн нэр      (заавал биш — хоосон бол «Захирал»)
//	4-р багана  Гарын үсэг зурагчийн регистр  (заавал — PIN2 энэ дугаарт очно)
//	5-р багана  Албан тушаал                  (заавал биш)
//
// Хоёрхон баганатай файл ч болно: нэр + гарын үсэг зурагчийн регистр.
// Эхний мөр нь гарчиг («нэр», «регистр» гэсэн үг агуулбал) бол алгасна.
//
// МӨР БҮР БИЕ ДААНА. Нэг мөрийн алдаа файлыг унагахгүй: 799 зөв мөр орж,
// 1 буруу нь ШАЛТГААНТАЙГАА буцна. Бүгд-эсвэл-юу-ч-биш байсан бол хүн том
// файлын алдааг нэг нэгээр нь хайж олох байсан.
package documents

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"

	"github.com/gerege-systems/open-gerege-nexus/backend/pkg/nexus"
)

// importBodyLimit нь жагсаалтын файлын дээд хэмжээ. 1000 мөрийн xlsx нь
// хэдхэн зуун КБ — 12MB бол маягтын биш, андуурч өгсөн файлын хэмжээ.
const importBodyLimit = 12 << 20

// importRowCap — нэг файлын дээд мөр. Түүнээс олон тал нэг гэрээнд байх нь
// өгөгдлийн биш, ойлголтын асуудал.
const importRowCap = 1000

type importSkip struct {
	Row    int    `json:"row"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

func (m *DocumentsModule) importPartiesHandler(w http.ResponseWriter, r *http.Request) {
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
	// addPartyHandler-тай ижил дүрэм, ижил шалтгаанаар: илгээгдсэн эсвэл
	// гарын үсэгтэй гэрээний талууд өөрчлөгдөхгүй.
	if shape.State != ContractDraft && shape.State != ContractNone {
		nexus.Error(w, http.StatusConflict, ErrNotDraft.Error())
		return
	}
	if shape.Signed > 0 {
		nexus.Error(w, http.StatusConflict,
			"энэ баримтад гарын үсэг зурагдсан тул талууд өөрчлөгдөхгүй")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, "файл ирсэнгүй — multipart 'file' талбарт өгнө үү")
		return
	}
	defer func() { _ = file.Close() }()

	rows, err := readRecipientRows(file, header.Filename)
	if err != nil {
		nexus.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(rows) == 0 {
		nexus.Error(w, http.StatusBadRequest, "файлд нэг ч мөр алга")
		return
	}
	if len(rows) > importRowCap {
		nexus.Error(w, http.StatusBadRequest,
			fmt.Sprintf("файлд %d мөр байна — нэг гэрээнд дээд тал нь %d тал", len(rows), importRowCap))
		return
	}

	added := 0
	skips := []importSkip{}
	actor := actorID(r.Context())
	for _, row := range rows {
		party, reason := m.importOneParty(r, tenantID, docID, row, actor)
		if reason != "" {
			skips = append(skips, importSkip{Row: row.line, Name: row.name, Reason: reason})
			continue
		}
		added++
		_ = party
	}

	// Гэрээ болсон агшин — addPartyHandler-ийн ижил мөч.
	if added > 0 && shape.State == ContractNone {
		if _, err := m.db.Exec(r.Context(),
			`UPDATE document_records SET contract_state = $3
			  WHERE id = $1 AND tenant_id = $2 AND contract_state = $4`,
			docID, tenantID, ContractDraft, ContractNone); err != nil {
			nexus.Error(w, http.StatusInternalServerError, "гэрээний төлөв шинэчлэгдсэнгүй")
			return
		}
	}

	nexus.Audit(r.Context(), tenantID, actorFor(r.Context()), "documents.parties_imported", docID,
		map[string]any{"added": added, "skipped": len(skips), "file": header.Filename})
	nexus.JSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skips})
}

func (m *DocumentsModule) importOneParty(r *http.Request, tenantID, docID string,
	row recipientRow, actor string) (Party, string) {

	req := partyRequest{
		Role:               RoleCounterparty,
		Kind:               KindOrganisation,
		DisplayName:        row.name,
		RegistrationNumber: row.orgReg,
	}
	// Байгууллагын регистргүй мөр нь ХҮН гэж уншигдана: хоёр баганатай
	// жагсаалт ихэвчлэн хүмүүсийнх.
	if row.orgReg == "" {
		req.Kind = KindPerson
	}
	if err := req.validate(); err != nil {
		return Party{}, err.Error()
	}
	if row.signerReg == "" {
		return Party{}, "гарын үсэг зурагчийн регистрийн дугаар алга — PIN2 хэнд ч очихгүй"
	}

	party, err := m.insertParty(r.Context(), tenantID, docID, req, actor)
	if err != nil {
		return Party{}, err.Error()
	}

	signerName := row.signerName
	if signerName == "" {
		signerName = "Захирал"
	}
	if _, err := m.db.Exec(r.Context(),
		`INSERT INTO document_party_signatories
		     (tenant_id, party_id, full_name, position, reg_number)
		 VALUES ($1, $2, $3, $4, $5)`,
		tenantID, party.ID, signerName, row.position,
		strings.ToUpper(row.signerReg)); err != nil {
		// Тал орсон, зурагч нь ороогүй — талыг нь буцааж авна: зурагчгүй
		// тал руу илгээж болох ч түүнийг санаатай, мөрөөрөө биш.
		_, _ = m.db.Exec(r.Context(),
			`DELETE FROM document_parties WHERE id = $1 AND state = 'draft'`, party.ID)
		return Party{}, "гарын үсэг зурагч бүртгэгдсэнгүй"
	}
	return party, ""
}

// recipientRow нь файлын нэг мөр, аль хэдийн цэвэрлэгдсэн.
type recipientRow struct {
	line       int
	name       string
	orgReg     string
	signerName string
	signerReg  string
	position   string
}

// readRecipientRows нь .xlsx эсвэл .csv-ээс мөрүүдийг уншина.
func readRecipientRows(file io.Reader, filename string) ([]recipientRow, error) {
	var raw [][]string
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".xlsx":
		book, err := excelize.OpenReader(file)
		if err != nil {
			return nil, fmt.Errorf("Excel файл уншигдсангүй: %v", err)
		}
		defer func() { _ = book.Close() }()
		sheets := book.GetSheetList()
		if len(sheets) == 0 {
			return nil, fmt.Errorf("Excel файлд хуудас алга")
		}
		// ЭХНИЙ хуудас — жагсаалт хоёрдугаар хуудсанд байдаг файл нь
		// ойлголцлын алдаа бөгөөд түүнийг таамгаар засахгүй.
		raw, err = book.GetRows(sheets[0])
		if err != nil {
			return nil, fmt.Errorf("Excel мөрүүд уншигдсангүй: %v", err)
		}
	case ".csv", ".txt":
		reader := csv.NewReader(file)
		reader.FieldsPerRecord = -1
		var err error
		raw, err = reader.ReadAll()
		if err != nil {
			return nil, fmt.Errorf("CSV уншигдсангүй: %v", err)
		}
	default:
		return nil, fmt.Errorf(".xlsx эсвэл .csv файл өгнө үү (ирсэн нь: %s)", filepath.Ext(filename))
	}

	rows := []recipientRow{}
	for i, cells := range raw {
		get := func(n int) string {
			if n < len(cells) {
				// Excel-ээс BOM, хатуу зай дагалдаж ирдэг.
				return strings.TrimSpace(strings.Trim(cells[n], "\ufeff\u00a0 "))
			}
			return ""
		}
		row := recipientRow{line: i + 1, name: get(0)}
		switch {
		case len(cells) >= 4:
			row.orgReg, row.signerName, row.signerReg, row.position = get(1), get(2), get(3), get(4)
		case len(cells) == 3:
			row.orgReg, row.signerReg = get(1), get(2)
		default:
			row.signerReg = get(1)
		}
		// Хоосон мөр — Excel-ийн сүүлчийн «мөр» ихэвчлэн ийм байдаг.
		if row.name == "" && row.signerReg == "" {
			continue
		}
		// Гарчгийн мөр: тоо биш, түлхүүр үгс.
		if i == 0 && looksLikeHeader(row) {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func looksLikeHeader(row recipientRow) bool {
	joined := strings.ToLower(row.name + " " + row.orgReg + " " + row.signerReg)
	for _, word := range []string{"нэр", "регистр", "name", "register", "байгууллага"} {
		if strings.Contains(joined, word) {
			return true
		}
	}
	return false
}

// importTemplateHandler нь бөглөх загварыг өгнө — багана нь юу гэсэн үг
// болохыг эндпойнт өөрөө хэлдэг байх нь заавар бичихээс найдвартай.
func (m *DocumentsModule) importTemplateHandler(w http.ResponseWriter, r *http.Request) {
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	sheet := book.GetSheetName(0)
	header := []any{"Байгууллагын нэр", "Байгууллагын регистр", "Гарын үсэг зурагчийн нэр",
		"Гарын үсэг зурагчийн регистр", "Албан тушаал"}
	example := []any{"ТЕСТ сургууль", "1234567", "Бат-Эрдэнэ", "АА00112233", "Захирал"}
	_ = book.SetSheetRow(sheet, "A1", &header)
	_ = book.SetSheetRow(sheet, "A2", &example)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="gerege-taluud.xlsx"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = book.Write(w)
}

// wordTemplateHandler нь бөглөж эхлэх Word загварыг өгнө.
func (m *DocumentsModule) wordTemplateHandler(w http.ResponseWriter, r *http.Request) {
	docx, err := contractTemplateDocx()
	if err != nil {
		nexus.Error(w, http.StatusInternalServerError, "загвар үүссэнгүй")
		return
	}
	w.Header().Set("Content-Type",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="gerege-zagvar.docx"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(docx)
}

// декодерын хэрэглээгүй сануулгаас зайлсхийе.
var _ = json.Marshal
