/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// Word нь бичвэрийг дур мэдэн хэсэгчилдэг: {{тал}} нь гурван run болж
// хадгалагдсан байх нь ердийн явдал. Орлуулга run-уудын хилийг давж олох
// ёстой — энэ тест хагалалтын хамгийн муу хэлбэрүүдийг барина.
func TestWordSubstitutionCrossesRunBoundaries(t *testing.T) {
	cases := []struct{ name, xml, want string }{
		{"бүтэн run",
			`<w:p><w:r><w:t>Тал: {{тал}} мөн</w:t></w:r></w:p>`,
			"Тал: Нийлүүлэгч ХХК мөн"},
		{"дундуураа хагарсан",
			`<w:p><w:r><w:t>Тал: {{та</w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>л}} мөн</w:t></w:r></w:p>`,
			"Тал: Нийлүүлэгч ХХК мөн"},
		{"гурав хуваагдсан",
			`<w:p><w:r><w:t>{{</w:t></w:r><w:r><w:t>тал</w:t></w:r><w:r><w:t>}}</w:t></w:r></w:p>`,
			"Нийлүүлэгч ХХК"},
		{"атрибуттай w:t",
			`<w:p><w:r><w:t xml:space="preserve">{{тал}} </w:t></w:r></w:p>`,
			"Нийлүүлэгч ХХК "},
		{"хоёр өөр түлхүүр",
			`<w:p><w:r><w:t>{{тал}} — {{ре</w:t></w:r><w:r><w:t>гистр}}</w:t></w:r></w:p>`,
			"Нийлүүлэгч ХХК — 5555555"},
		{"давтагдсан түлхүүр",
			`<w:p><w:r><w:t>{{тал}} ба {{тал}}</w:t></w:r></w:p>`,
			"Нийлүүлэгч ХХК ба Нийлүүлэгч ХХК"},
	}
	f := Fields{SchoolName: "Нийлүүлэгч ХХК", SchoolCode: "5555555"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := substituteWordXML(tc.xml, substitutionPairs(f))
			text := visibleText(got)
			if text != tc.want {
				t.Errorf("бичвэр %q болов, %q хүлээсэн\nXML: %s", text, tc.want, got)
			}
			// XML бүтэц эвдрээгүй: тагийн тоо хэвээр.
			if strings.Count(got, "<w:t") != strings.Count(tc.xml, "<w:t") {
				t.Errorf("run-ы тоо өөрчлөгдөв: %s", got)
			}
		})
	}
}

// Утга нь ӨӨРИЙНХӨӨ түлхүүрийг агуулж болно: «{{тал}}» гэдэг үг нэртэйгээ
// орсон байгууллага орлуулгыг мөнхийн давталтад оруулж байв — нэг гүйлтийн
// орлуулга ЭХ бичвэрээс л хайдаг тул оруулсан утгаа дахин хайхгүй.
func TestWordSubstitutionValueContainingItsOwnKeyTerminates(t *testing.T) {
	f := Fields{SchoolName: "«{{тал}}» ХХК", SchoolCode: "{{регистр}}"}
	xml := `<w:p><w:r><w:t>Тал: {{тал}} ({{регистр}})</w:t></w:r></w:p>`

	done := make(chan string, 1)
	go func() { done <- substituteWordXML(xml, substitutionPairs(f)) }()
	select {
	case got := <-done:
		want := "Тал: «{{тал}}» ХХК ({{регистр}})"
		if text := visibleText(got); text != want {
			t.Errorf("бичвэр %q болов, %q хүлээсэн", text, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("орлуулга 5 секундэд дуусаагүй — мөнхийн давталт")
	}
}

// Орлуулах утга нь XML-д аюулгүй байх ёстой: & агуулсан нэр баримтыг
// эвдэхгүй.
func TestWordSubstitutionEscapesTheValue(t *testing.T) {
	got := substituteWordXML(`<w:p><w:r><w:t>{{тал}}</w:t></w:r></w:p>`,
		substitutionPairs(Fields{SchoolName: `А & Б <ХХК> "мөн"`}))
	if !strings.Contains(got, "А &amp; Б &lt;ХХК&gt; &quot;мөн&quot;") {
		t.Errorf("утга escape хийгдсэнгүй: %s", got)
	}
}

// Бүтэн docx: задлаад, орлуулаад, буцааж уншихад зөв гарах ёстой — бусад
// файлууд нь байтаараа хэвээр.
func TestSubstituteDocxRoundTrip(t *testing.T) {
	template, err := contractTemplateDocx()
	if err != nil {
		t.Fatal(err)
	}
	out, err := substituteDocx(template, Fields{
		SchoolName: "ТЕСТ-2 сургууль", SchoolCode: "2222299",
		Principal: "Дорж", ContractCode: "EDU-2026/T9",
		Date: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := docxText(t, out)
	for _, want := range []string{"ТЕСТ-2 сургууль", "2222299", "Дорж", "EDU-2026/T9"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q орлуулагдсангүй; бичвэр: %.200s", want, text)
		}
	}
	if strings.Contains(text, "{{тал}}") || strings.Contains(text, "{{регистр}}") {
		t.Error("placeholder үлдчихэв")
	}
}

// isWordTemplate нь нэр ба байтын АЛЬ АЛИЙГ шаардана.
func TestIsWordTemplate(t *testing.T) {
	docx, _ := contractTemplateDocx()
	if !isWordTemplate("geree.docx", docx) {
		t.Error("жинхэнэ docx танигдсангүй")
	}
	if isWordTemplate("geree.docx", []byte("энгийн текст")) {
		t.Error("docx нэртэй текст Word гэж танигдав")
	}
	if isWordTemplate("geree.pdf", docx) {
		t.Error("pdf нэртэй zip Word гэж танигдав")
	}
}

func visibleText(xml string) string {
	var b strings.Builder
	for _, m := range wordTextRe.FindAllStringSubmatch(xml, -1) {
		b.WriteString(m[1])
	}
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'").
		Replace(b.String())
}

func docxText(t *testing.T, content []byte) string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range reader.File {
		if entry.Name != "word/document.xml" {
			continue
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		return visibleText(string(data))
	}
	t.Fatal("word/document.xml алга")
	return ""
}
