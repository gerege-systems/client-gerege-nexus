/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// WORD ЗАГВАР: гэрээ Word дээр бичигддэг, гарын үсэг PDF дээр зурагддаг.
//
// Админ гэрээгээ хэвийнхээрээ Word дээр бэлдэнэ — хүснэгт, дугаарлалт, толгой
// хэсэгтэйгээ. Тараах агшинд хүлээн авагч бүрийн хувь дээр ЯГ ТҮҮНИЙ нэр,
// регистр орлуулагдаж, тэр даруй PDF болж хөлддөг. Гарын үсэг ямагт PDF дээр:
// Word файл компьютер бүр дээр өөр өөрөөр харагддаг тул «би юуг зурсан бэ»
// гэдэг асуултын хариулт байж чадахгүй; PDF нь байт нь.
//
// # ОРЛУУЛГА ЯАГААД ЭНГИЙН REPLACE БИШ ВЭ
//
// .docx бол ZIP доторх XML. Word нь бичвэрийг дур мэдэн ХЭСЭГЧИЛЖ хадгалдаг:
// {{тал}} гэж бичсэн нь дотроо `<w:t>{{та</w:t>...<w:t>л}}</w:t>` болчихсон
// байх нь ердийн явдал — зөв бичгийн шалгуур, форматын өөрчлөлт, бүр курсорын
// байрлал ч run-ыг хагалдаг. Тиймээс орлуулга нь run-уудын ХИЛИЙГ ДАВЖ
// хайдаг: бүх <w:t>-ийн бичвэрийг нэг мөр болгож нийлүүлээд, түлхүүрийг тэнд
// олоод, оролцсон run бүрээс өөрийнх нь хэсгийг нь авч, орлуулах утгыг эхний
// run-д нь суулгана. XML бүтэц өөрөө хэвээр үлдэнэ.
package documents

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// isWordTemplate нь хавсралт Word загвар мөн эсэхийг хэлнэ. Нэр нь .docx ба
// байт нь ZIP («PK») — аль нэг нь дангаараа хангалтгүй: .docx нэртэй text
// файл ч, өргөтгөлөө алдсан жинхэнэ docx ч байдаг.
func isWordTemplate(name string, content []byte) bool {
	return strings.HasSuffix(strings.ToLower(name), ".docx") &&
		len(content) > 4 && content[0] == 'P' && content[1] == 'K'
}

// substituteDocx нь загварын placeholder-уудыг талын мэдээллээр орлуулж,
// ШИНЭ docx буцаана. Бусад бүх зүйл — зураг, загвар, тохиргоо — байтаараа
// хэвээр хуулагдана.
func substituteDocx(content []byte, f Fields) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("загварын Word файл нээгдсэнгүй: %w", err)
	}

	pairs := substitutionPairs(f)
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, entry := range reader.File {
		data, err := readZipEntry(entry)
		if err != nil {
			return nil, err
		}
		// Бичвэр амьдардаг хэсгүүд: үндсэн баримт, толгой, хөл, тайлбар.
		if isWordTextPart(entry.Name) {
			data = []byte(substituteWordXML(string(data), pairs))
		}
		w, err := writer.CreateHeader(&zip.FileHeader{Name: entry.Name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readZipEntry(entry *zip.File) ([]byte, error) {
	r, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

func isWordTextPart(name string) bool {
	if name == "word/document.xml" {
		return true
	}
	return strings.HasPrefix(name, "word/header") || strings.HasPrefix(name, "word/footer") ||
		name == "word/footnotes.xml" || name == "word/endnotes.xml"
}

// wordTextRe нь Word-ийн харагдах бичвэрийн атом: <w:t>...</w:t>.
var wordTextRe = regexp.MustCompile(`<w:t(?: [^>]*)?>([^<]*)</w:t>`)

// substituteWordXML нь нэг XML хэсэгт бүх хосыг орлуулна.
//
// НЭГ ГҮЙЛТ: бүх тохиолдлыг ЭХ бичвэрээс эхэлж олоод, дараа нь баруунаас
// зүүн тийш нэг мөсөн сольдог. Ингэснээр орлуулсан УТГЫГ хэзээ ч дахин
// хайхгүй — «Нийлүүлэгч {{тал}} ХХК» гэдэг нэртэй байгууллага орлуулгыг
// мөнхийн давталтад оруулж чадахгүй (эх утга нь хэвээр, дахин хайлт үгүй).
func substituteWordXML(doc string, pairs []string) string {
	matches := wordTextRe.FindAllStringSubmatchIndex(doc, -1)
	if len(matches) == 0 {
		return doc
	}

	// Харагдах бичвэр ба run бүрийн (doc-координат, full-координат) зураглал.
	segs := make([]wordSeg, len(matches))
	var full strings.Builder
	for i, m := range matches {
		segs[i] = wordSeg{m[2], m[3], full.Len()}
		full.WriteString(doc[m[2]:m[3]])
	}
	text := full.String()

	// Бүх түлхүүрийн бүх тохиолдлыг ЭХ бичвэрээс. Давхцахгүй: олдсон мужийг
	// эзэлж тэмдэглэнэ, дараагийн түлхүүр эзлэгдсэн газраас олдвол алгасна.
	taken := make([]bool, len(text))
	var edits []wordEdit
	for i := 0; i+1 < len(pairs); i += 2 {
		key, value := pairs[i], xmlEscape(pairs[i+1])
		for from := 0; ; {
			idx := strings.Index(text[from:], key)
			if idx < 0 {
				break
			}
			start := from + idx
			end := start + len(key)
			from = end
			overlaps := false
			for j := start; j < end; j++ {
				if taken[j] {
					overlaps = true
					break
				}
			}
			if overlaps {
				continue
			}
			for j := start; j < end; j++ {
				taken[j] = true
			}
			edits = append(edits, wordEdit{start, end, value})
		}
	}
	if len(edits) == 0 {
		return doc
	}

	// Баруунаас зүүн тийш: өмнөх засвар дараагийнхын координатыг хөдөлгөхгүй.
	sortEditsDesc(edits)
	for _, e := range edits {
		doc = applyEditAcrossRuns(doc, segs, e.start, e.end, e.value)
	}
	return doc
}

type wordSeg struct{ docStart, docEnd, fullStart int }

type wordEdit struct {
	start, end int
	value      string
}

func sortEditsDesc(edits []wordEdit) {
	for i := 1; i < len(edits); i++ {
		for j := i; j > 0 && edits[j].start > edits[j-1].start; j-- {
			edits[j], edits[j-1] = edits[j-1], edits[j]
		}
	}
}

// applyEditAcrossRuns нь full-координатын [start,end) мужийг value-ээр
// сольж, оролцсон run бүрээс өөрийнх нь хэсгийг авна. XML бүтэц хэвээр.
func applyEditAcrossRuns(doc string, segs []wordSeg, start, end int, value string) string {

	var b strings.Builder
	prev, inserted := 0, false
	for _, s := range segs {
		segLen := s.docEnd - s.docStart
		fullEnd := s.fullStart + segLen
		lo, hi := start, end
		if s.fullStart > lo {
			lo = s.fullStart
		}
		if fullEnd < hi {
			hi = fullEnd
		}
		if lo >= hi {
			continue
		}
		removeStart := s.docStart + (lo - s.fullStart)
		removeEnd := s.docStart + (hi - s.fullStart)
		b.WriteString(doc[prev:removeStart])
		if !inserted {
			b.WriteString(value)
			inserted = true
		}
		prev = removeEnd
	}
	b.WriteString(doc[prev:])
	return b.String()
}

// xmlEscape — орлуулах утга нь БИЧВЭР, XML биш: нэрэндээ & агуулсан
// байгууллага баримтыг эвдэх ёсгүй.
func xmlEscape(value string) string {
	return strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;",
	).Replace(value)
}

// ─────────────────────────────────────────────── загвар .docx

// contractTemplateDocx нь бөглөж эхлэх Word загварыг барина.
//
// Хамгийн бага бүтэцтэй жинхэнэ .docx: Word ч, LibreOffice ч нээнэ. Админ
// агуулгаа бичээд placeholder-уудыг байранд нь үлдээхэд л тараалт хүн бүрийн
// хувийг өөрийнх нь мэдээллээр бөглөнө. Заавар нь баримтын дотроо — тусдаа
// бичиг хэн ч уншдаггүй.
func contractTemplateDocx() ([]byte, error) {
	paragraph := func(text string, bold bool) string {
		props := ""
		if bold {
			props = "<w:rPr><w:b/></w:rPr>"
		}
		return `<w:p><w:r>` + props + `<w:t xml:space="preserve">` + xmlEscape(text) + `</w:t></w:r></w:p>`
	}
	var body strings.Builder
	for _, line := range []struct {
		text string
		bold bool
	}{
		{"ГЭРЭЭ {{дугаар}}", true},
		{"", false},
		{"{{огноо}}", false},
		{"", false},
		{"Нэг талаас БАЙГУУЛЛАГЫН НЭРИЙГ ЭНД БИЧНЭ, нөгөө талаас {{тал}} ({{регистр}}), төлөөлж {{төлөөлөгч}} нар дараах гэрээг байгуулав.", false},
		{"", false},
		{"1. ЭНД ГЭРЭЭНИЙХЭЭ ЗААЛТУУДЫГ БИЧНЭ.", false},
		{"2. …", false},
		{"", false},
		{"— Доорх бичиглэлүүд тараах үед хүлээн авагч бүрийн мэдээллээр автоматаар солигдоно: {{тал}} = нэр, {{регистр}} = регистр, {{төлөөлөгч}} = гарын үсэг зурагч, {{дугаар}} = гэрээний дугаар, {{огноо}} = өнөөдрийн огноо. Энэ тайлбар мөрийг устгаарай.", false},
	} {
		body.WriteString(paragraph(line.text, line.bold))
	}

	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		body.String() + `<w:sectPr/></w:body></w:document>`

	files := []struct{ name, content string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", document},
	}

	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, file := range files {
		w, err := writer.CreateHeader(&zip.FileHeader{Name: file.name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(file.content)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
