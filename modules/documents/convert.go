/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

// WORD → PDF: Gotenberg (LibreOffice) хөрвүүлэгчээр.
//
// Go дотор Word-ийг ҮНЭНЧЭЭР PDF болгох арга байхгүй — Word бол дүрслэлийн
// хөдөлгүүр шаарддаг формат. Gotenberg нь LibreOffice-ийг боосон бэлэн HTTP
// үйлчилгээ: docx явуулна, PDF ирнэ. Байрлуулалт нь compose-ийн нэг
// контейнер, гаднаас хүрэгдэхгүй.
//
// Хөрвүүлэгчгүй байрлуулалт нь эвдэрсэн биш — Word зам нь ажиллахгүй гэдгээ
// ТОДОРХОЙ хэлдэг байрлуулалт: PDF ба бичвэрийн зам хэвээр.
package documents

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"
)

// convertTimeout нь нэг хөрвүүлэлтийн дээд хугацаа. Том, зурагтай гэрээ
// LibreOffice-д хэдэн секунд зарцуулдаг; 90с нь «удаж байна» ба «унжсан»
// хоёрын зааг.
const convertTimeout = 90 * time.Second

// gotenbergURL нь хөрвүүлэгчийн хаяг. Compose дотор нэрээрээ.
func gotenbergURL() string {
	if url := os.Getenv("GOTENBERG_URL"); url != "" {
		return url
	}
	return "http://gotenberg:3000"
}

// convertDocxToPDF нь Word баримтыг PDF болгоно.
func convertDocxToPDF(ctx context.Context, content []byte, filename string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, convertTimeout)
	defer cancel()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("files", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(content); err != nil {
		return nil, err
	}
	if err := form.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		gotenbergURL()+"/forms/libreoffice/convert", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Word→PDF хөрвүүлэгчид хүрсэнгүй: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("Word→PDF хөрвүүлэлт %d: %s", res.StatusCode, detail)
	}
	pdf, err := io.ReadAll(io.LimitReader(res.Body, int64(100<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(pdf) < 5 || string(pdf[:5]) != "%PDF-" {
		return nil, fmt.Errorf("хөрвүүлэгч PDF биш зүйл буцаав")
	}
	return pdf, nil
}
