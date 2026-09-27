package handlers

import (
	"reflect"
	"testing"
)

func TestUniqueNonEmptyStrings(t *testing.T) {
	got := uniqueNonEmptyStrings(
		[]string{"ข้อจำกัดจาก Settings", "", "  ข้อมูล Jira ยังไม่พร้อม  "},
		[]string{"ข้อจำกัดจาก Settings", "ข้อมูลเพิ่มเติม"},
	)
	want := []string{"ข้อจำกัดจาก Settings", "ข้อมูล Jira ยังไม่พร้อม", "ข้อมูลเพิ่มเติม"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueNonEmptyStrings() = %#v, want %#v", got, want)
	}
}
