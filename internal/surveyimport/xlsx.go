// Package surveyimport reads the four-column Excel workbook used for anonymous HR imports.
package surveyimport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
)

const MaxFileBytes = 5 << 20
const MaxRows = 250
const maxPartBytes = 12 << 20

type Row struct {
	Number     int
	Department string
	Position   string
	Score      int16
	Comment    string
}

var headers = [4]map[string]bool{
	{"department": true, "department name": true, "แผนก": true, "ชื่อแผนก": true},
	{"position": true, "ตำแหน่ง": true, "ตำแหน่งงาน": true},
	{"score": true, "satisfaction score": true, "คะแนน": true, "คะแนนความพึงพอใจ": true, "ความพึงพอใจ": true},
	{"comment": true, "feedback": true, "ข้อความ": true, "ความคิดเห็น": true, "ข้อเสนอแนะ": true, "ความคิดเห็นและข้อเสนอแนะ": true},
}

// Parse reads the first worksheet only. All reported row numbers match Excel's row numbers.
func Parse(data []byte) ([]Row, []string, error) {
	if len(data) == 0 || len(data) > MaxFileBytes {
		return nil, nil, fmt.Errorf("upload an .xlsx file no larger than %d MB", MaxFileBytes>>20)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, errors.New("file is not a valid .xlsx workbook")
	}
	parts := make(map[string]*zip.File, len(z.File))
	for _, file := range z.File {
		parts[file.Name] = file
	}
	workbook, err := readPart(parts, "xl/workbook.xml")
	if err != nil {
		return nil, nil, errors.New("workbook is missing xl/workbook.xml")
	}
	rels, err := readPart(parts, "xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, nil, errors.New("workbook sheet relationships are missing")
	}
	sheetPath, err := firstSheetPath(workbook, rels)
	if err != nil {
		return nil, nil, err
	}
	sheet, err := readPart(parts, sheetPath)
	if err != nil {
		return nil, nil, errors.New("first worksheet could not be read")
	}
	shared := []string{}
	if parts["xl/sharedStrings.xml"] != nil {
		part, err := readPart(parts, "xl/sharedStrings.xml")
		if err != nil {
			return nil, nil, err
		}
		shared, err = readSharedStrings(part)
		if err != nil {
			return nil, nil, err
		}
	}
	return readRows(sheet, shared)
}

func readPart(parts map[string]*zip.File, name string) ([]byte, error) {
	file := parts[name]
	if file == nil || file.UncompressedSize64 > maxPartBytes {
		return nil, errors.New("workbook part is missing or too large")
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxPartBytes+1))
	if err != nil || len(data) > maxPartBytes {
		return nil, errors.New("workbook part is too large or damaged")
	}
	return data, nil
}

func firstSheetPath(workbook, rels []byte) (string, error) {
	type sheet struct {
		Attributes []xml.Attr `xml:",any,attr"`
	}
	var book struct {
		Sheets []sheet `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(workbook, &book); err != nil || len(book.Sheets) == 0 {
		return "", errors.New("workbook has no readable first sheet")
	}
	id := ""
	for _, attr := range book.Sheets[0].Attributes {
		if attr.Name.Local == "id" {
			id = attr.Value
		}
	}
	var relationships struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(rels, &relationships); err != nil || id == "" {
		return "", errors.New("first worksheet relationship is invalid")
	}
	for _, relation := range relationships.Items {
		if relation.ID != id {
			continue
		}
		target := relation.Target
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else if !strings.HasPrefix(target, "xl/") {
			target = path.Join("xl", target)
		}
		if !strings.HasPrefix(target, "xl/worksheets/") {
			return "", errors.New("first worksheet path is invalid")
		}
		return target, nil
	}
	return "", errors.New("first worksheet relationship was not found")
}

func readSharedStrings(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	values := []string{}
	inItem, inText := false, false
	var current strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return values, nil
		}
		if err != nil {
			return nil, errors.New("shared strings are invalid")
		}
		switch item := token.(type) {
		case xml.StartElement:
			if item.Name.Local == "si" {
				inItem = true
				current.Reset()
			} else if inItem && item.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				current.Write(item)
			}
		case xml.EndElement:
			if item.Name.Local == "t" {
				inText = false
			} else if item.Name.Local == "si" {
				values = append(values, current.String())
				inItem = false
			}
		}
	}
}

func readRows(data []byte, shared []string) ([]Row, []string, error) {
	type cell struct {
		Ref    string `xml:"r,attr"`
		Type   string `xml:"t,attr"`
		Value  string `xml:"v"`
		Inline struct {
			Text string `xml:"t"`
			Runs []struct {
				Text string `xml:"t"`
			} `xml:"r"`
		} `xml:"is"`
	}
	type sheetRow struct {
		Number int    `xml:"r,attr"`
		Cells  []cell `xml:"c"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rows := make([]Row, 0)
	issues := make([]string, 0)
	headerSeen := false
	rowOrdinal := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, errors.New("worksheet XML is invalid")
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		var raw sheetRow
		if err := decoder.DecodeElement(&raw, &start); err != nil {
			return nil, nil, errors.New("worksheet row is invalid")
		}
		rowOrdinal++
		if raw.Number == 0 {
			raw.Number = rowOrdinal
		}
		values := [4]string{}
		extra := false
		for cellPosition, c := range raw.Cells {
			column := columnIndex(c.Ref)
			if column < 0 {
				column = cellPosition
			}
			value := c.Value
			if c.Type == "inlineStr" {
				value = c.Inline.Text
				for _, run := range c.Inline.Runs {
					value += run.Text
				}
			} else if c.Type == "s" {
				index, err := strconv.Atoi(c.Value)
				if err != nil || index < 0 || index >= len(shared) {
					return nil, nil, errors.New("worksheet contains an invalid shared string")
				}
				value = shared[index]
			}
			if column >= 4 {
				extra = extra || strings.TrimSpace(value) != ""
			} else {
				values[column] = strings.TrimSpace(value)
			}
		}
		if raw.Number == 1 {
			headerSeen = true
			for i, value := range values {
				normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
				if !headers[i][normalized] && !(i == 3 && strings.HasPrefix(normalized, "ความคิดเห็นและข้อเสนอแนะเกี่ยวกับการทำงาน")) {
					issues = append(issues, fmt.Sprintf("column %c has an unexpected header: %q", 'A'+i, value))
				}
			}
			if extra {
				issues = append(issues, "the workbook must have exactly four columns (A-D)")
			}
			continue
		}
		if values == [4]string{} && !extra {
			continue
		}
		if len(rows) >= MaxRows {
			issues = append(issues, fmt.Sprintf("workbook exceeds %d data rows", MaxRows))
			break
		}
		if extra {
			issues = append(issues, fmt.Sprintf("row %d: unexpected data after column D", raw.Number))
		}
		score, err := strconv.ParseFloat(values[2], 64)
		if err != nil || score < 1 || score > 5 || math.Trunc(score) != score {
			issues = append(issues, fmt.Sprintf("row %d: score must be a whole number from 1 to 5", raw.Number))
		}
		if values[0] == "" {
			issues = append(issues, fmt.Sprintf("row %d: department is required", raw.Number))
		}
		if values[3] == "" {
			issues = append(issues, fmt.Sprintf("row %d: comment is required", raw.Number))
		}
		rows = append(rows, Row{Number: raw.Number, Department: values[0], Position: values[1], Score: int16(score), Comment: values[3]})
	}
	if !headerSeen {
		issues = append(issues, "row 1 must contain the four column headers")
	}
	if len(rows) == 0 {
		issues = append(issues, "workbook has no data rows")
	}
	return rows, issues, nil
}

func columnIndex(ref string) int {
	index := 0
	for _, char := range ref {
		if char < 'A' || char > 'Z' {
			break
		}
		index = index*26 + int(char-'A'+1)
	}
	return index - 1
}
