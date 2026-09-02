/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fields() Fields {
	return Fields{
		SchoolName:   "Туршилтын сургууль",
		SchoolCode:   "TEST-01",
		Aimag:        "Улаанбаатар",
		Soum:         "Баянзүрх",
		Address:      "Туршилтын хаяг",
		Principal:    "Соронзонболд Сэнгүм",
		AcademicYear: "2026-2027",
		ContractCode: "EDU-2026/001",
		Title:        "Хамтын ажиллагааны гэрээ",
		Date:         time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
	}
}

func TestSubstituteFillsEverySchoolField(t *testing.T) {
	// Орлуулагч бүр ажиллах ёстой: нэг нь дуугүй ажиллахаа болих нь гэрээнд
	// хоосон зай биш, БУРУУ сургуулийн нэр үлдээж болзошгүй.
	body := "{{сургууль}}|{{код}}|{{аймаг}}|{{сум}}|{{хаяг}}|{{захирал}}|{{жил}}|{{дугаар}}|{{огноо}}"
	got := Substitute(body, fields())
	for _, want := range []string{
		"Туршилтын сургууль", "TEST-01", "Улаанбаатар", "Баянзүрх",
		"Туршилтын хаяг", "Соронзонболд Сэнгүм", "2026-2027", "EDU-2026/001",
		"2026 оны 08 сарын 24",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("орлуулалтад %q алга: %s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("орлуулагдаагүй тэмдэг үлдэв: %s", got)
	}
}

func TestSubstituteLeavesUnknownPlaceholderVisible(t *testing.T) {
	// Танихгүй орлуулагчийг устгавал гэрээ бичсэн хүн алдаагаа мэдэхгүй.
	got := Substitute("{{албан_тушаал}} ба {{сургууль}}", fields())
	if !strings.Contains(got, "{{албан_тушаал}}") {
		t.Errorf("танихгүй орлуулагч арчигдав: %s", got)
	}
}

func TestRenderProducesSignableCyrillicPDF(t *testing.T) {
	f := fields()
	body := "Нэг талаас Боловсролын газар, нөгөө талаас {{сургууль}} нар " +
		"№ 4 маягтын дагуу гэрээ байгуулав. Ө Ү ё — «жишээ»."
	pdf, sum, err := Render(f.Title, body, f, []SignatureBlock{{
		Label: "Захиалагч", Name: "Соронзонболд Сэнгүм", Etsi: "PNOMN-110770722023",
		At: f.Date, SHA256: strings.Repeat("ab", 32),
	}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Гарын үсгийн рельс эдгээрийг шаарддаг: `validatePDF` толгойг, стемплэгч
	// нь эмх цэгцтэй xref-ийг.
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Errorf("PDF толгой буруу: %q", pdf[:8])
	}
	tail := pdf[max(0, len(pdf)-2048):]
	if !bytes.Contains(tail, []byte("%%EOF")) {
		t.Error("сүүлийн 2 КБ-д PDF төгсгөлийн тэмдэг алга")
	}
	if !bytes.Contains(pdf, []byte("\nxref")) {
		t.Error("сонгодог xref хүснэгт алга")
	}
	if len(sum) != 64 {
		t.Errorf("SHA-256 нь 64 тэмдэгт байх ёстой, %d байна", len(sum))
	}

	// Кирилл нь шигтгэсэн фонтоор л гарна: суурь 14 фонт Ө, Ү, №-г кодлож
	// чадахгүй тул шигтгээгүй бол гэрээ уншигдахгүй хэвлэгдэнэ.
	if !bytes.Contains(pdf, []byte("/FontFile2")) {
		t.Error("шигтгэсэн TrueType фонт алга")
	}
	if !bytes.Contains(pdf, []byte("Identity-H")) {
		t.Error("Identity-H кодчилол алга — кирилл гарахгүй")
	}
	if !bytes.Contains(pdf, []byte("/ToUnicode")) {
		t.Error("ToUnicode алга — гэрээнээс текст хуулж, хайж болохгүй")
	}

	// Тайлалт хоосон бол тестийг алгасахгүй — УНАНА. Хоосон дээр "шалгах юм
	// алга" гэж өнгөрөх нь фонт эвдэрсэн өдөр ногоон хэвээр байх гэсэн үг.
	text := extractPDFText(t, pdf)
	if text == "" {
		t.Fatal("PDF-ээс бичвэр тайлагдсангүй — ToUnicode буулгалт эвдэрсэн байна")
	}
	for _, want := range []string{
		"Туршилтын сургууль", "Соронзонболд Сэнгүм", "PNOMN-110770722023",
		"Ө", "Ү", "№", "«жишээ»",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF-ийн бичвэрт %q алга", want)
		}
	}
	// Орлуулагч нь PDF рүү орох ёсгүй.
	if strings.Contains(text, "{{") {
		t.Error("орлуулагдаагүй тэмдэг PDF-д үлдэв")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	// Гарын үсэг БАЙТЫГ хамардаг. Ижил гэрээ хоёр өөр байт өгвөл "би юуг
	// зурсан бэ" гэдэгт хариулт үлдэхгүй.
	f := fields()
	a, sumA, err := Render(f.Title, "{{сургууль}} гэрээ", f, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, sumB, err := Render(f.Title, "{{сургууль}} гэрээ", f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sumA != sumB || !bytes.Equal(a, b) {
		t.Errorf("PDF давтагдахгүй байна: %s vs %s", sumA, sumB)
	}
}

func TestRenderDiffersPerSchool(t *testing.T) {
	// Хоёр сургуулийн хувь ижил байт байвал нэг нь нөгөөгийнхөө гарын үсгийг
	// хүлээж авах болно.
	f := fields()
	a, sumA, _ := Render(f.Title, "{{сургууль}}", f, nil)
	g := f
	g.SchoolName = "Өөр сургууль"
	b, sumB, _ := Render(g.Title, "{{сургууль}}", g, nil)
	if sumA == sumB || bytes.Equal(a, b) {
		t.Error("өөр сургуулийн хувь ижил байт өглөө")
	}
}

func TestFileNameStaysASCII(t *testing.T) {
	// Content-Disposition-д кирилл нэр орвол зарим хөтөч файлыг нэргүй
	// хадгалдаг; гэрээний дугаар нь тэмдэгттэй байж болно.
	got := fileName("EDU-2026/001", "TEST-01")
	if strings.ContainsAny(got, "/\\ ") {
		t.Errorf("нэрэнд зам эсвэл зай үлдэв: %q", got)
	}
	if !strings.HasSuffix(got, ".pdf") {
		t.Errorf(".pdf өргөтгөлгүй: %q", got)
	}
	if fileName("", "x") == "-x.pdf" {
		t.Error("хоосон дугаарт нөөц нэр алга")
	}
}

// extractPDFText нь PDF-ийн бичвэрийг ҮНЭХЭЭР тайлж буцаана.
//
// Энэ нь энгийн хайлт биш байх ёстой: Identity-H кодчилолд бичвэр нь глифийн
// дугаараар бичигддэг тул урсгалаас "Ө" гэж хайвал шигтгээ ажиллаж байгаа
// эсэхээс үл хамааран ХЭЗЭЭ Ч олдохгүй. Өөрөөр хэлбэл гэнэн хайлт нь фонт
// эвдэрсэн үед ч, зөв үед ч ижил хариу өгнө — юу ч шалгахгүй гэсэн үг.
//
// Тиймээс ToUnicode буулгалтыг уншиж, агуулгын урсгал дахь кодыг үсэг рүү
// буцаана. Энэ нь Acrobat дотор хуулах, хайх үед болдог яг тэр үйлдэл.
func extractPDFText(t *testing.T, pdf []byte) string {
	t.Helper()
	streams := inflateAll(pdf)
	// ToUnicode CMap нь fpdf-д ШАХАГДААГҮЙ бичигддэг тул зөвхөн задарсан
	// урсгалаас хайвал хэзээ ч олдохгүй. Түүхий байтыг бас хайлтад оруулна.
	cmapSources := append(append([]string{}, streams...), string(pdf))

	// 1. ToUnicode: `<кодоос> <юникод>` хосууд, мөн мужууд.
	//
	// Мужийг ДЭЛГЭХГҮЙ. fpdf нь `<0000> <FFFF> <0000>` гэсэн бүтэн identity
	// муж бичдэг бөгөөд түүнийг оруулга болгон дэлгэвэл 65 536 мөр болно.
	// Урьд нь энэ давталт 4096-аар тасалдаг байсан ба тэр нь зөвхөн U+0FFF
	// хүртэлх тэмдэгтийг буулгаж, № (U+2116), — (U+2014) зэргийг "алга" гэж
	// худал мэдээлж байв. Кирилл нь U+04xx тул анзаарагдалгүй өнгөрсөн.
	// Мужийг мужаараа хайх нь хязгаарыг бүрмөсөн арилгана.
	type bfRange struct {
		lo, hi uint32
		dst    rune
	}
	var ranges []bfRange
	toUni := map[uint16]rune{}
	lookup := func(code uint16) (rune, bool) {
		if r, ok := toUni[code]; ok {
			return r, true
		}
		for _, g := range ranges {
			if uint32(code) >= g.lo && uint32(code) <= g.hi {
				return g.dst + rune(uint32(code)-g.lo), true
			}
		}
		return 0, false
	}
	bfchar := regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar`)
	bfrange := regexp.MustCompile(`(?s)beginbfrange(.*?)endbfrange`)
	pair := regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
	triple := regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
	for _, s := range cmapSources {
		for _, block := range bfchar.FindAllStringSubmatch(s, -1) {
			for _, mm := range pair.FindAllStringSubmatch(block[1], -1) {
				if code, err := strconv.ParseUint(mm[1], 16, 32); err == nil {
					toUni[uint16(code)] = hexToRune(mm[2])
				}
			}
		}
		for _, block := range bfrange.FindAllStringSubmatch(s, -1) {
			for _, mm := range triple.FindAllStringSubmatch(block[1], -1) {
				lo, e1 := strconv.ParseUint(mm[1], 16, 32)
				hi, e2 := strconv.ParseUint(mm[2], 16, 32)
				if e1 != nil || e2 != nil || hi < lo {
					continue
				}
				ranges = append(ranges, bfRange{uint32(lo), uint32(hi), hexToRune(mm[3])})
			}
		}
	}
	if len(toUni) == 0 && len(ranges) == 0 {
		return ""
	}

	// 2. Агуулгын урсгал дахь `(…) Tj` мөрүүд, 2 байтаар.
	var out strings.Builder
	show := regexp.MustCompile(`\(((?:[^()\\]|\\.)*)\)\s*Tj`)
	for _, s := range streams {
		for _, mm := range show.FindAllStringSubmatch(s, -1) {
			raw := unescapePDFString(mm[1])
			for i := 0; i+1 < len(raw); i += 2 {
				if r, ok := lookup(uint16(raw[i])<<8 | uint16(raw[i+1])); ok {
					out.WriteRune(r)
				}
			}
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func hexToRune(h string) rune {
	// UTF-16BE. Гэрээнд surrogate шаардах тэмдэгт байхгүй тул эхний нэгжийг авна.
	if len(h) < 4 {
		return 0
	}
	v, err := strconv.ParseUint(h[:4], 16, 32)
	if err != nil {
		return 0
	}
	return rune(v)
}

func unescapePDFString(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out = append(out, s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j, v := i, 0
			for ; j < len(s) && j-i < 3 && s[j] >= '0' && s[j] <= '7'; j++ {
				v = v*8 + int(s[j]-'0')
			}
			out = append(out, byte(v))
			i = j - 1
		default:
			out = append(out, s[i])
		}
	}
	return out
}

func inflateAll(pdf []byte) []string {
	var out []string
	re := regexp.MustCompile(`stream\r?\n`)
	for _, loc := range re.FindAllIndex(pdf, -1) {
		start := loc[1]
		end := bytes.Index(pdf[start:], []byte("endstream"))
		if end < 0 {
			continue
		}
		zr, err := zlib.NewReader(bytes.NewReader(pdf[start : start+end]))
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(zr)
		_ = zr.Close()
		if err == nil {
			out = append(out, string(raw))
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestRenderSurvivesCharactersOutsideTheBasicPlane(t *testing.T) {
	// fpdf-ийн өргөний хүснэгт 65 536 нүдтэй тул U+FFFF-ээс дээш руна түүний
	// гадуур заадаг. Ийм тэмдэгт нэг сургуулийн хаягт орсноос болж БҮХ
	// сургуулийн гэрээ үүсэхгүй байх ёсгүй.
	f := fields()
	f.Address = "Хаяг 🏫 байр"
	f.SchoolName = "Сургууль 𝕏"
	pdf, sum, err := Render("Гэрээ 🎓", "{{сургууль}} — {{хаяг}}", f, []SignatureBlock{{
		Label: "Захиалагч", Name: "Нэр 😀",
	}})
	if err != nil {
		t.Fatalf("BMP-ээс гадуурх тэмдэгт гэрээг унагав: %v", err)
	}
	if len(sum) != 64 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("PDF бүрэн гарсангүй")
	}
	text := extractPDFText(t, pdf)
	if !strings.Contains(text, "Сургууль") || !strings.Contains(text, "Хаяг") {
		t.Errorf("эргэн тойрны бичвэр алдагдав: %q", text)
	}
}

// Ерөнхий орлуулгууд нь ерөнхий гэрээнд ажиллах ёстой.
//
// `render.go` энэ репод eduge-ээс зөөгдөж ирсэн бөгөөд орлуулгын нэрс нь
// сургуулийнхаар үлдсэн байв: нийлүүлэгчтэй гэрээ бичиж буй хүнд «нөгөө
// талын нэр» гэсэн орлуулга байхгүй, зөвхөн `{{сургууль}}` байсан.
func TestGeneralPlaceholdersNameWhatTheySubstitute(t *testing.T) {
	f := Fields{
		SchoolName: "Нийлүүлэгч ХХК", SchoolCode: "5555555",
		Principal: "Ганбат", Title: "Нийлүүлэлтийн гэрээ",
		Address: "СБД 1-р хороо", ContractCode: "2026/07",
	}
	body := "{{тал}} | {{регистр}} | {{төлөөлөгч}} | {{гэрээ}} | {{хаяг}} | {{дугаар}}"
	got := Substitute(body, f)
	for _, want := range []string{
		"Нийлүүлэгч ХХК", "5555555", "Ганбат", "Нийлүүлэлтийн гэрээ",
		"СБД 1-р хороо", "2026/07",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q орлуулагдсангүй: %q", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("орлуулагдаагүй хаалт үлдэв: %q", got)
	}
}

// Сургуулийн хуучин нэрс АЖИЛЛАСААР байх ёстой: аль хэдийн бичигдсэн
// бичвэрийг эвдэх нь тэднийг дахин бичүүлэхээс дор.
func TestTheOlderSchoolPlaceholdersStillWork(t *testing.T) {
	got := Substitute("{{сургууль}} / {{захирал}}",
		Fields{SchoolName: "12-р сургууль", Principal: "Дорж"})
	if got != "12-р сургууль / Дорж" {
		t.Errorf("хуучин орлуулга эвдэрсэн: %q", got)
	}
}
