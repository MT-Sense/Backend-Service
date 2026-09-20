package analytics

import (
	"strings"
	"testing"
)

func TestSafeSampleQuotesRedactsAndDeduplicates(t *testing.T) {
	quotes := safeSampleQuotes([]string{
		"ระบบธนาคารช้า ติดต่อ foo@example.com",
		"ระบบธนาคารช้า ติดต่อ foo@example.com",
		"[ถูกปกปิด]",
		"ทีมแก้ปัญหาได้เร็ว",
	})
	if len(quotes) != 2 {
		t.Fatalf("expected two distinct useful quotes, got %v", quotes)
	}
	if strings.Contains(quotes[0], "foo@example.com") {
		t.Fatal("sample quote exposed an email address")
	}
}
