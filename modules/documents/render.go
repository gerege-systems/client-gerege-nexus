/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-pdf/fpdf"
)

// Фонт нь binary дотор явна.
//
// Кирилл үсэг PDF-ийн суурь 14 фонтод БАЙХГҮЙ: тэдгээр нь WinAnsi бөгөөд Ө, Ү,
// № зэрэг тэмдэгтийг кодлож чадахгүй. Тиймээс TrueType-ийг шигтгэнэ.
//
// Яагаад системийн фонтод найдаагүй вэ: runtime образ нь alpine бөгөөд түүнд
// фонт байхгүй. `apk add font-noto` нэмж болох байсан ч тэр нь "гэрээ
// хэвлэгдэхгүй" гэсэн алдааг образын Dockerfile-аас хамааруулна — өөрөөр
// хэлбэл кодоос хол, гэрээ зурах гэж байгаа хүнээс хол. Binary дотор байвал
// хөрвүүлэгдсэн зүйл ажиллана.
//
// Noto Sans нь Монгол кирилл 71 тэмдэгтийг бүрэн хамарна (Ө ө Ү ү Ё ё №,
// cmap-аас шалгасан), OFL-1.1 лицензтэй — лиценз нь хажууд нь.
//
//go:embed assets/NotoSans-Regular.ttf
var fontFS embed.FS

var (
	fontOnce sync.Once
	fontTTF  []byte
	fontErr  error
)

func font() ([]byte, error) {
	fontOnce.Do(func() { fontTTF, fontErr = fontFS.ReadFile("assets/NotoSans-Regular.ttf") })
	return fontTTF, fontErr
}

// Fields нь гэрээний бичвэрт орлуулагдах утгууд.
//
// Орлуулагчийн нэр монголоор байгаа нь санаатай: гэрээ бичдэг хүн бол хуульч,
// программист биш. `{{сургууль}}` гэж бичих нь `{{school_name}}`-аас сурахад
// хялбар бөгөөд буруу бичсэн нь дэлгэц дээр шууд харагдана.
type Fields struct {
	SchoolName   string
	SchoolCode   string
	Aimag        string
	Soum         string
	Address      string
	Principal    string
	AcademicYear string
	ContractCode string
	Title        string
	Date         time.Time
}

var monthNames = [...]string{
	"01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12",
}

func (f Fields) dateText() string {
	if f.Date.IsZero() {
		return ""
	}
	return fmt.Sprintf("%d оны %s сарын %02d", f.Date.Year(), monthNames[int(f.Date.Month())-1], f.Date.Day())
}

// Substitute нь загварыг тухайн сургуулийн утгуудаар нөхнө.
//
// Танихгүй орлуулагчийг ХӨНДӨХГҮЙ орхино. Устгавал гэрээнд хоосон зай үлдэж,
// бичсэн хүн бичвэрээ буруу гэж мэдэхгүй; байрандаа үлдвэл дэлгэц дээр
// `{{албан_тушаал}}` гэж харагдаж, засах шаардлагатайг өөрөө хэлнэ.
func Substitute(body string, f Fields) string {
	repl := []string{
		// ЕРӨНХИЙ НЭРС. Энэ апп нь сургуулийнх биш — тал нь нийлүүлэгч,
		// түрээслэгч, яам байж болно — тул орлуулга нь юуг орлуулж байгаагаа
		// нэрлэх ёстой. Доорх сургуулийн нэрс нь энэ файл eduge-ээс
		// зөөгдөж ирсний үлдэц бөгөөд ажилласаар байна: аль хэдийн бичигдсэн
		// бичвэрийг эвдэх нь тэднийг дахин бичүүлэхээс дор.
		"{{тал}}", f.SchoolName,
		"{{регистр}}", f.SchoolCode,
		"{{төлөөлөгч}}", f.Principal,
		"{{гэрээ}}", f.Title,

		// Сургуулийн нэрс — хуучин бичвэрүүдийн төлөө.
		"{{сургууль}}", f.SchoolName,
		"{{код}}", f.SchoolCode,
		"{{аймаг}}", f.Aimag,
		"{{сум}}", f.Soum,
		"{{хаяг}}", f.Address,
		"{{захирал}}", f.Principal,
		"{{жил}}", f.AcademicYear,
		"{{дугаар}}", f.ContractCode,
		"{{гэрчилгээ}}", f.Title,
		"{{огноо}}", f.dateText(),
	}
	return strings.NewReplacer(repl...).Replace(body)
}

// SignatureBlock нь аль хэдийн зурагдсан гарын үсгийн гэрчлэл — сургуулийн хувь
// дээр хэвлэгдэнэ.
//
// Яагаад хэвлэдэг вэ: eID нь дижитал гарын үсэг тутамд нэг PIN2 шаарддаг тул
// төв байгууллага 800 сургуулийн хувь тус бүрд гарын үсэг зурж чадахгүй
// (`handlers_batch.go`: "there is no way to have a citizen approve a set of
// documents with one PIN entry"). Тиймээс төв тал МАСТЕР дээр нэг удаа зурж,
// сургуулийн хувь нь тэр гарын үсгийн гэрчлэлийг — хэн, хэзээ, ямар хешийн
// дээр — бичвэр болгон агуулна. Захирлын PIN2 нь тэр гэрчлэлийг ХАМРАН зурна,
// өөрөөр хэлбэл захирал "төв тал ийм гарын үсэг зурсан" гэдгийг батална.
type SignatureBlock struct {
	Label  string
	Name   string
	Etsi   string
	At     time.Time
	SHA256 string
}

// bmpOnly нь Юникодын үндсэн хавтгайгаас (BMP) гадуурх тэмдэгтийг орлуулна.
//
// fpdf-ийн тэмдэгтийн өргөний хүснэгт нь яг 65 536 нүдтэй (`utf8fontfile.go`,
// `make([]int, 256*256)`) бөгөөд түүнээс дээш кодтой руна тэр хүснэгтийн
// гадуур заана. Өөрөөр хэлбэл эможи бүхий нэг сургуулийн хаяг гэрээ ҮҮСГЭХ
// ажлыг бүхэлд нь унагана. Ийм тэмдэгт гэрээнд байх учиргүй тул түүнийг
// орлуулж, ажлыг үргэлжлүүлэх нь зөв — үлдсэн 799 сургууль нэг эможигоос
// болж гэрээгүй үлдэх ёсгүй.
//
// Identity-H нь UTF-16-ийн нэг нэгжээр кодлодог тул энэ хязгаар нь зөвхөн
// fpdf-ийн биш, форматын өөрийнх нь хязгаар.
func bmpOnly(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r > 0xFFFF }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r > 0xFFFF {
			return '�'
		}
		return r
	}, s)
}

func (f Fields) sanitised() Fields {
	f.SchoolName = bmpOnly(f.SchoolName)
	f.SchoolCode = bmpOnly(f.SchoolCode)
	f.Aimag = bmpOnly(f.Aimag)
	f.Soum = bmpOnly(f.Soum)
	f.Address = bmpOnly(f.Address)
	f.Principal = bmpOnly(f.Principal)
	f.AcademicYear = bmpOnly(f.AcademicYear)
	f.ContractCode = bmpOnly(f.ContractCode)
	f.Title = bmpOnly(f.Title)
	return f
}

// Render нь гэрээний PDF-ийг үүсгэж, байт ба SHA-256-г буцаана.
//
// Хеш нь ЯГ ЭНЭ байтуудын хеш: eID нь ижил хешийг буцаадаг тул гарын үсэг
// үнэхээр бидний үүсгэсэн бичвэрийг хамарсан эсэхийг тулгаж шалгаж болно.
func Render(title, body string, f Fields, blocks []SignatureBlock) ([]byte, string, error) {
	title, body, f = bmpOnly(title), bmpOnly(body), f.sanitised()
	for i := range blocks {
		blocks[i].Name = bmpOnly(blocks[i].Name)
		blocks[i].Label = bmpOnly(blocks[i].Label)
	}
	ttf, err := font()
	if err != nil {
		return nil, "", fmt.Errorf("гэрээний фонт уншигдсангүй: %w", err)
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddUTF8FontFromBytes("Noto", "", ttf)
	pdf.SetMargins(20, 18, 20)
	pdf.SetAutoPageBreak(true, 18)

	// Гарын үсэг нь БАЙТЫГ хамардаг тул үүсгэлт давтагдах чанартай байх ёстой:
	// анхдагч `/CreationDate` нь дуудлага тутам өөр байт өгөх ба тэр нь ижил
	// гэрээг хоёр удаа зурахад хоёр өөр хеш гарна гэсэн үг. Огноог гэрээний
	// өөрийн огноо болгож тогтооно.
	stamp := f.Date
	if stamp.IsZero() {
		stamp = time.Unix(0, 0).UTC()
	}
	pdf.SetCreationDate(stamp.UTC())
	pdf.SetModificationDate(stamp.UTC())

	pdf.AddPage()

	pdf.SetFont("Noto", "", 15)
	pdf.MultiCell(0, 8, strings.ToUpper(title), "", "C", false)
	pdf.Ln(3)

	pdf.SetFont("Noto", "", 9.5)
	head := []string{}
	if f.ContractCode != "" {
		head = append(head, "Гэрээний дугаар: "+f.ContractCode)
	}
	if d := f.dateText(); d != "" {
		head = append(head, "Огноо: "+d)
	}
	if f.SchoolName != "" {
		head = append(head, "Сургууль: "+f.SchoolName+placeIn(f.SchoolCode))
	}
	if f.AcademicYear != "" {
		head = append(head, "Хичээлийн жил: "+f.AcademicYear)
	}
	if len(head) > 0 {
		pdf.MultiCell(0, 5, strings.Join(head, "\n"), "", "L", false)
		pdf.Ln(3)
	}

	pdf.SetFont("Noto", "", 10.5)
	pdf.MultiCell(0, 5.6, Substitute(body, f), "", "L", false)

	if len(blocks) > 0 {
		pdf.Ln(6)
		pdf.SetFont("Noto", "", 11)
		pdf.MultiCell(0, 6, "ТАЛУУДЫН ГАРЫН ҮСЭГ", "", "L", false)
		pdf.SetFont("Noto", "", 9)
		for _, b := range blocks {
			lines := []string{
				b.Label + ": " + b.Name,
			}
			if b.Etsi != "" {
				lines = append(lines, "  Иргэний дугаар: "+b.Etsi)
			}
			if !b.At.IsZero() {
				lines = append(lines, "  Зурсан: "+b.At.UTC().Format("2006-01-02 15:04:05")+" UTC")
			}
			if b.SHA256 != "" {
				lines = append(lines, "  Баримтын SHA-256: "+b.SHA256)
			}
			lines = append(lines, "  eID Mongolia-гийн тоон гарын үсгээр баталгаажсан.")
			pdf.MultiCell(0, 4.8, strings.Join(lines, "\n"), "", "L", false)
			pdf.Ln(2)
		}
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, "", fmt.Errorf("PDF үүсгэхэд: %w", err)
	}
	b := buf.Bytes()
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

func placeIn(code string) string {
	if code == "" {
		return ""
	}
	return " (" + code + ")"
}

// fileName нь Content-Disposition-д аюулгүй ASCII нэр өгнө: кирилл нэр орвол
// зарим хөтөч файлыг нэргүй хадгалдаг.
func fileName(code, suffix string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, code)
	if safe == "" {
		safe = "geree"
	}
	return safe + "-" + suffix + ".pdf"
}
