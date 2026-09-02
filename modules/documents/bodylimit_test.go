/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Хүсэлтийн биеийн хязгаар нь МАРШРУТ ТУТАМД байх ёстой, бүлэг дээр биш.
//
// `limitBody` нь модулийн бүх бүлэг дээр 64 КБ тавьдаг байв — JSON хүсэлтэд
// зөв тоо. Гэвч `POST /{id}/file` нь PDF хүлээж авдаг бөгөөд өөрийн 34 МБ
// уншигчийг тавьдаг. Тэр нь юуг ч өргөсгөж чадахгүй: бүлгийн middleware
// `r.ContentLength > 64 КБ` дээр аль хэдийн 413 буцаасан байх ба `r.Body`-г
// 64 КБ `MaxBytesReader`-ээр ороосон байна. `MaxBytesReader`-ийг дахин
// ороовол ДОТООД хязгаар нь хүчинтэй.
//
// Үр дүн: 64 КБ-аас том PDF-ийг баримтад хавсаргах боломжгүй байсан. Гэрээ
// байгуулах гол зам дээрх алдаа бөгөөд түүнийг барих тест байгаагүй.
func TestAttachingAPDFLargerThanTheJSONLimitIsAccepted(t *testing.T) {
	router := routerFor(t, "documents.manage")

	// 200 КБ — жинхэнэ гэрээний PDF-ийн ердийн хэмжээ, JSON-ы хязгаараас том.
	pdf := make([]byte, 200<<10)
	copy(pdf, []byte("%PDF-1.4\n"))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "contract.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(pdf); err != nil {
		t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/documents/33333333-3333-3333-3333-333333333333/file", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// Энэ тест сан руу хүрдэггүй (routerFor-ийн pool нь хаана ч холбогдоогүй)
	// тул амжилтыг хүлээхгүй. Шалгах зүйл ганц: хүсэлт ХЭМЖЭЭНИЙХЭЭ УЧРААС
	// татгалзаагүй байх. 413 бол тэр алдаа.
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("64 КБ-аас том PDF хэмжээнийхээ учраас татгалзагдав (413) — "+
			"гэрээний PDF хавсаргах боломжгүй байна: %s", rec.Body.String())
	}
}

// JSON маршрутууд хязгаараа ХАДГАЛНА. Дээрх засвар нь бүх хамгаалалтыг
// нээчихвэл 444 МБ-ын workflow хүсэлт дахин боломжтой болно — тэр нь энэ
// хязгаарыг оруулсан анхны шалтгаан.
func TestJSONRoutesKeepTheirSmallBodyLimit(t *testing.T) {
	router := routerFor(t, "documents.manage")

	huge := bytes.NewReader(make([]byte, 1<<20)) // 1 МБ JSON
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/", huge)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("1 МБ JSON хүсэлт татгалзагдсангүй (%d) — биеийн хязгаар алдагдсан байна", rec.Code)
	}
}
