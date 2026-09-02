/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"os"
	"strings"
	"testing"

	domain "github.com/gerege-systems/client-gerege-nexus/domain/documents"
)

// Гарын үсэг ЯМАР байтыг хамарснаа үнэн нэрлэх ёстой.
//
// `document_files.sha256` нь ЭХ хувийн хеш — `fileOf` өөрөө тэгж хэлдэг:
// "the signed copy has bytes added to it by design, and its digest is not the
// one recorded". Харин PAdES нь гарын үсгийг баримт дээр НЭМДЭГ тул хоёр дахь
// гарын үсэг зурагч `pdfToSign`-аас нэгэнт гарын үсэгтэй болсон файлыг авдаг.
//
// Тиймээс ёслолд илгээсэн байтын хеш нь эх хувийнхаас ӨӨР бөгөөд бүртгэлд
// эх хувийнхыг бичвэл 2 дахь гарын үсгээс эхлээд `covered_digest` нь тэр
// гарын үсэг хамрааагүй байтыг нэрлэнэ. Маргаан яг энэ талбар дээр тулдаг.
func TestCoveredDigestNamesTheBytesActuallySent(t *testing.T) {
	original := []byte("%PDF-1.4\nанхны баримт\n")
	onceSigned := append(append([]byte{}, original...), []byte("<<гарын үсэг 1>>")...)

	if domain.Digest(original) == domain.Digest(onceSigned) {
		t.Fatal("туршилтын өгөгдөл буруу: хоёр байт ижил хештэй байна")
	}

	// startContentSignature-ийн PAdES салаа нь `sentDigest`-ийг илгээсэн
	// байтаас бодох ёстой. Энэ нь тэр гэрээг эх кодын түвшинд барина:
	// хэрэв хэн нэгэн `sentDigest = artifact.SHA256` рүү буцаавал энэ тест
	// унана.
	src := sourceOf(t, "contentsign.go")
	if !strings.Contains(src, "sentDigest = domain.Digest(pdf)") {
		t.Error("PAdES салаанд илгээсэн байтын хеш бодогдохгүй байна — " +
			"covered_digest эх хувийг нэрлэх эрсдэлтэй")
	}
	if strings.Contains(src, "artifact.SHA256, string(format)); err != nil") {
		t.Error("ёслолын мөрд эх хувийн хеш бичигдсэн хэвээр байна")
	}
}

// Эрхгүй хүний гарын үсэг баримтад суух ёсгүй.
//
// Рельсээс ирсэн, гарын үсэг шигтгэсэн PDF нь урьд нь `checkSigner`-ээс ӨМНӨ
// `document_files.signed_content`-д бичигддэг байв. Дарааллын үр дагавар:
// дарааллын нэрлээгүй хүн PIN2-оороо зөвшөөрөхөд гарын үсэг нь татгалзагдсан
// ч файл нь үлдэж, `pdfToSign` дараагийн хүнд ЯГ ТЭР файлыг өгдөг байсан.
//
// Одоо тэр бичилт гарын үсгийн гүйлгээ дотор, эрхийн шалгалтын дараа явна.
func TestSignedPDFIsStoredOnlyAfterTheAuthorityCheck(t *testing.T) {
	poll := sourceOf(t, "contentsign.go")
	// Ёслолын хариу нь зөвхөн БАРИГДАНА, хадгалагдахгүй.
	if strings.Contains(poll, "m.keepSignedPDF(") {
		t.Error("гарын үсэгтэй PDF эрхийн шалгалтаас өмнө хадгалагдсаар байна")
	}
	if !strings.Contains(poll, "signedPDF = signed.PDF") {
		t.Error("гарын үсэгтэй PDF гүйлгээ рүү дамжуулагдахгүй байна")
	}

	// Бичилт нь гүйлгээ дотор, `checkSigner`-ийн ДАРАА байх ёстой.
	write := sourceOf(t, "documents.go")
	check := strings.Index(write, "if err := checkSigner(position, docType, signature.RegNumber")
	store := strings.Index(write, "UPDATE document_files SET signed_content")
	if check < 0 || store < 0 {
		t.Fatal("writeSignature-ийн эрхийн шалгалт эсвэл файлын бичилт олдсонгүй")
	}
	if store < check {
		t.Error("гарын үсэгтэй PDF нь эрхийн шалгалтаас ӨМНӨ бичигдэж байна")
	}
	if !strings.Contains(write[check:store], "tx.Exec") && !strings.Contains(write[store:store+200], "tx.Exec") {
		t.Error("файлын бичилт гарын үсгийн гүйлгээнээс гадуур байна")
	}
}

// sourceOf нь модулийн эх файлыг уншина. Эдгээр тест нь ажиллах зан төлөвийг
// биш, КОДЫН ДАРААЛЛЫГ барьдаг: гурван алдаа гурвуулаа сан руу хүрдэг тул
// нэгжийн тестээр гүйцэд туршихад жинхэнэ Postgres, жинхэнэ eID рельс
// хэрэгтэй. Дараалал нь харин эх кодоос уншигдана — тэр нь эргэж ирэхээс
// хамгаалах хамгийн хямд хашлага.
func sourceOf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s уншигдсангүй: %v", name, err)
	}
	return string(b)
}
