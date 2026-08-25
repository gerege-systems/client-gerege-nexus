/*
 * Gerege Client
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation.
 * Distributed under the Apache 2.0 License.
 */

package documents

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// Excel-ийн жагсаалт мөр мөрөөрөө зөв уншигдах ёстой.
func TestRecipientRowsFromExcel(t *testing.T) {
	book := excelize.NewFile()
	sheet := book.GetSheetName(0)
	rows := [][]any{
		{"Байгууллагын нэр", "Регистр", "Захирлын нэр", "Захирлын регистр", "Албан тушаал"},
		{"1-р сургууль", "1111111", "Дорж", "АА11223344", "Захирал"},
		{"2-р сургууль", "2222222", "", "ББ55667788", ""},
		{}, // хоосон мөр — Excel-ийн сүүлч ихэвчлэн ийм
	}
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := book.SetSheetRow(sheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := book.Write(&buf); err != nil {
		t.Fatal(err)
	}

	got, err := readRecipientRows(&buf, "taluud.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("2 мөр хүлээсэн, %d ирэв: %+v", len(got), got)
	}
	if got[0].name != "1-р сургууль" || got[0].orgReg != "1111111" ||
		got[0].signerName != "Дорж" || got[0].signerReg != "АА11223344" || got[0].position != "Захирал" {
		t.Errorf("эхний мөр буруу: %+v", got[0])
	}
	if got[1].signerReg != "ББ55667788" || got[1].signerName != "" {
		t.Errorf("хоёр дахь мөр буруу: %+v", got[1])
	}
}

// CSV — хоёр баганатай хамгийн бага хэлбэр: нэр + регистр.
func TestRecipientRowsFromMinimalCSV(t *testing.T) {
	csv := "Нэр,Регистр\nДорж,АА11223344\nДулмаа,ББ55667788\n"
	got, err := readRecipientRows(strings.NewReader(csv), "taluud.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("2 мөр хүлээсэн, %d ирэв", len(got))
	}
	if got[0].name != "Дорж" || got[0].signerReg != "АА11223344" {
		t.Errorf("эхний мөр буруу: %+v", got[0])
	}
	// Хоёр баганатай мөрөнд байгууллагын регистр байхгүй — тэр нь importOneParty
	// дээр ХҮН гэж уншигдана.
	if got[0].orgReg != "" {
		t.Errorf("хоёр баганатай мөрөнд байгууллагын регистр гарч ирэв: %q", got[0].orgReg)
	}
}

// Гарчиггүй файл — эхний мөр нь өгөгдөл бол алгасагдах ёсгүй.
func TestRecipientRowsWithoutHeader(t *testing.T) {
	csv := "1-р сургууль,1111111,Дорж,АА11223344,Захирал\n"
	got, err := readRecipientRows(strings.NewReader(csv), "t.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].name != "1-р сургууль" {
		t.Fatalf("өгөгдлийн мөр гарчиг гэж алгасагдав: %+v", got)
	}
}

// Танихгүй өргөтгөл — тодорхой хариулттай татгалзана.
func TestRecipientRowsRejectsUnknownFormat(t *testing.T) {
	if _, err := readRecipientRows(strings.NewReader("x"), "taluud.pdf"); err == nil {
		t.Fatal("PDF-ийг жагсаалт гэж хүлээж авав")
	}
}
