package dto

import (
	"strings"
	"testing"
)

func TestModerateRequestReply(t *testing.T) {
	reply := "  ขอบคุณที่แจ้ง HR กำลังดูเรื่องนี้  "
	req := ModerateRequest{HRReply: &reply}
	if req.IsEmpty() {
		t.Fatal("a reply alone is an update")
	}
	if problems := req.Validate(); len(problems) != 0 || *req.HRReply != "ขอบคุณที่แจ้ง HR กำลังดูเรื่องนี้" {
		t.Fatalf("problems=%v reply=%q", problems, *req.HRReply)
	}

	long := strings.Repeat("ก", MaxHRReplyLength+1)
	if problems := (&ModerateRequest{HRReply: &long}).Validate(); len(problems) != 1 {
		t.Fatalf("expected the over-long reply to be rejected, got %v", problems)
	}
}
