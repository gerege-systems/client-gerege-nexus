/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents_test

import (
	"testing"

	documents "github.com/gerege-systems/client-gerege-nexus/domain/documents"
)

// Том PDF хавсаргагдана — гэхдээ PAdES-ээр биш.
//
// Гарын үсгийн рельс (core-ийн `maxPDFBytes`, платформын esign) 25 МБ дээр
// татгалздаг. Урьд нь хавсаргах тааз түүнтэй тэнцүү байсан бөгөөд үр дүн нь
// том гэрээг ОГТ хавсаргуулахгүй байх явдал байв. Одоо хавсаргана, харин
// хеш дээр нь гарын үсэг зурна.
func TestALargePDFIsAttachableAndSignedDetached(t *testing.T) {
	small := documents.Artifact{ContentType: "application/pdf", SHA256: "x",
		SizeBytes: documents.MaxEmbeddedSignatureBytes}
	if got := documents.FormatFor(small, true); got != documents.FormatPAdES {
		t.Errorf("25 МБ хүртэлх PDF нь PAdES байх ёстой, %q ирэв", got)
	}

	large := documents.Artifact{ContentType: "application/pdf", SHA256: "x",
		SizeBytes: documents.MaxEmbeddedSignatureBytes + 1}
	if got := documents.FormatFor(large, true); got != documents.FormatDetached {
		t.Errorf("25 МБ-аас том PDF нь detached байх ёстой, %q ирэв — "+
			"PAdES-ээр илгээвэл рельс татгалзана", got)
	}

	// Хавсаргах тааз нь гарын үсгийнхээс ӨНДӨР байх ёстой, эс бөгөөс том
	// гэрээг огт оруулж чадахгүй.
	if documents.MaxArtifactBytes <= documents.MaxEmbeddedSignatureBytes {
		t.Error("хавсаргах тааз нь гарын үсгийн таазаас өндөр байх ёстой")
	}
	if err := documents.CheckAttachable(0, make([]byte, documents.MaxEmbeddedSignatureBytes+1)); err != nil {
		t.Errorf("25 МБ-аас том баримт хавсаргагдсангүй: %v", err)
	}
	if err := documents.CheckAttachable(0, make([]byte, documents.MaxArtifactBytes+1)); err == nil {
		t.Error("таазаас давсан баримт хүлээн авагдав")
	}
}
