package surveyimport

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

func workbook(t *testing.T, sheet string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook><sheets><sheet name="Survey" sheetId="1" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData>` + sheet + `</sheetData></worksheet>`,
	}
	for name, body := range parts {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func headerRow() string {
	return `<row r="1"><c r="A1" t="inlineStr"><is><t>Department</t></is></c><c r="B1" t="inlineStr"><is><t>ตำแหน่ง</t></is></c><c r="C1" t="inlineStr"><is><t>คะแนน</t></is></c><c r="D1" t="inlineStr"><is><t>ข้อความ</t></is></c></row>`
}

func TestParseFourColumnWorkbook(t *testing.T) {
	sheet := headerRow() + `<row r="2"><c r="A2" t="inlineStr"><is><t>Engineering</t></is></c><c r="B2" t="inlineStr"><is><t>SA</t></is></c><c r="C2"><v>4</v></c><c r="D2" t="inlineStr"><is><t>ทีมทำงานดีขึ้น</t></is></c></row>`
	rows, issues, err := Parse(workbook(t, sheet))
	if err != nil || len(issues) != 0 || len(rows) != 1 {
		t.Fatalf("unexpected parse: rows=%+v issues=%v err=%v", rows, issues, err)
	}
	if rows[0].Number != 2 || rows[0].Department != "Engineering" || rows[0].Position != "SA" || rows[0].Score != 4 || rows[0].Comment != "ทีมทำงานดีขึ้น" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestParseRejectsInvalidScoreAndExtraColumn(t *testing.T) {
	sheet := headerRow() + `<row r="3"><c r="A3" t="inlineStr"><is><t>Engineering</t></is></c><c r="C3"><v>6</v></c><c r="D3" t="inlineStr"><is><t>ข้อความ</t></is></c><c r="E3"><v>1</v></c></row>`
	_, issues, err := Parse(workbook(t, sheet))
	if err != nil || len(issues) != 2 || !strings.Contains(strings.Join(issues, " "), "row 3") {
		t.Fatalf("unexpected validation: %v, %v", issues, err)
	}
}

func TestParseRejectsWrongHeaderOrder(t *testing.T) {
	sheet := strings.Replace(headerRow(), ">Department<", ">คะแนน<", 1)
	_, issues, err := Parse(workbook(t, sheet))
	if err != nil || len(issues) == 0 {
		t.Fatalf("expected header issue, got %v, %v", issues, err)
	}
}

func TestParseWorkbookWrittenByOpenpyxl(t *testing.T) {
	data, err := os.ReadFile("testdata/example.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	rows, issues, err := Parse(data)
	if err != nil || len(issues) != 0 || len(rows) != 1 || rows[0].Comment != "ทีมทำงานดีขึ้น" {
		t.Fatalf("unexpected openpyxl workbook parse: rows=%+v issues=%v err=%v", rows, issues, err)
	}
}
