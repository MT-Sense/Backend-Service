package privacy

import (
	"strings"
	"testing"

	"github.com/mt-sense/backend-service/internal/models"
)

func TestRedactRemovesContactDetails(t *testing.T) {
	cases := []struct {
		name string
		in   string
		leak string // fragment that must NOT survive redaction
	}{
		{"thai mobile", "ติดต่อฉันที่ 081-234-5678 นะ", "081-234-5678"},
		{"thai mobile no dashes", "โทร 0812345678", "0812345678"},
		{"international", "call +66812345678", "+66812345678"},
		{"email", "ส่งมาที่ somchai.j@company.co.th ได้เลย", "somchai.j@company.co.th"},
		{"national id", "เลขบัตร 1-2345-67890-12-3", "1-2345-67890-12-3"},
		{"employee id en", "my code is EMP-01234 thanks", "EMP-01234"},
		{"employee id th", "รหัสพนักงาน 04812 ครับ", "04812"},
		{"thai honorific name", "หัวหน้าคุณสมชายไม่เคยฟัง", "คุณสมชาย"},
		{"latin honorific name", "Mr. Somchai never listens", "Somchai"},
		{"url", "ดูที่ https://facebook.com/someone", "facebook.com"},
		{"handle", "ทักมาที่ @somchai_dev ได้", "@somchai_dev"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if strings.Contains(got, tc.leak) {
				t.Errorf("Redact(%q) = %q — still leaks %q", tc.in, got, tc.leak)
			}
			if !strings.Contains(got, redactionMark) {
				t.Errorf("Redact(%q) = %q — expected a redaction mark", tc.in, got)
			}
		})
	}
}

// Thai is written without spaces, so a name pattern cannot know where the name ends.
// The deliberate trade-off is to over-redact rather than leak: the name must go, and a
// few characters of the following word may go with it. What must NOT happen is the match
// running away and consuming the whole complaint, which is what an unbounded pattern does.
func TestRedactBoundsThaiNameOverreach(t *testing.T) {
	in := "หัวหน้าคุณสมชายไม่เคยฟังความเห็นของทีม"
	got := Redact(in)

	if strings.Contains(got, "สมชาย") {
		t.Errorf("Redact(%q) = %q — the name survived", in, got)
	}
	// The tail of the sentence must still be there; only the name plus a short run may go.
	if !strings.Contains(got, "ความเห็นของทีม") {
		t.Errorf("Redact(%q) = %q — redaction ran away and ate the complaint", in, got)
	}
}

func TestRedactKeepsOrdinaryFeedback(t *testing.T) {
	// The substance of a complaint must survive; over-redaction destroys the product.
	in := "งานกะดึกหนักเกินไป อยากให้กระจายให้เท่ากันมากกว่านี้"
	if got := Redact(in); got != in {
		t.Errorf("Redact(%q) = %q — ordinary feedback was altered", in, got)
	}
}

func TestSuppressHidesSmallGroups(t *testing.T) {
	if got := Suppress(4, 3.9); !got.Suppressed {
		t.Error("group of 4 must be suppressed (n<5 rule)")
	}
	if got := Suppress(models.MinGroupSize, 3.9); got.Suppressed {
		t.Errorf("group of %d must be visible", models.MinGroupSize)
	}
	if got := Suppress(76, 4.2); got.Suppressed || got.Data != 4.2 {
		t.Errorf("large group should pass the value through, got %+v", got)
	}
}

func TestSuppressibleJSONShapeMatchesFrontend(t *testing.T) {
	hidden, err := Suppress(1, 3.9).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(hidden) != `{"suppressed":true}` {
		t.Errorf("hidden shape = %s, frontend expects {\"suppressed\":true}", hidden)
	}

	shown, err := Suppress(10, 3.9).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(shown) != `{"suppressed":false,"data":3.9}` {
		t.Errorf("visible shape = %s", shown)
	}
}

func TestLooksIdentifying(t *testing.T) {
	flagged := []string{
		"กรุณากรอกชื่อ-นามสกุล",
		"รหัสพนักงานของคุณคือ?",
		"What is your full name?",
		"Please provide your phone number",
	}
	for _, q := range flagged {
		if !LooksIdentifying(q) {
			t.Errorf("LooksIdentifying(%q) = false, want true", q)
		}
	}

	ok := []string{
		"คุณพอใจกับภาระงานแค่ไหน?",
		"How satisfied are you with your team?",
	}
	for _, q := range ok {
		if LooksIdentifying(q) {
			t.Errorf("LooksIdentifying(%q) = true, want false", q)
		}
	}
}
