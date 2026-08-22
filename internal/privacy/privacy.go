// Package privacy holds the two rules the whole product rests on: no aggregate over a
// group smaller than five people ever leaves the server, and no open text is persisted
// before personally identifying fragments are stripped out of it.
package privacy

import (
	"regexp"
	"strings"

	"github.com/mt-sense/backend-service/internal/models"
)

// ---------------------------------------------------------------------------
// n < 5 suppression
// ---------------------------------------------------------------------------

// Suppress wraps a grouped value, hiding it when the group had fewer than
// models.MinGroupSize respondents. Every handler that groups by department, team or
// tenure must route its values through here — the SQL side additionally filters with
// HAVING COUNT(*) >= ?, so a group below the threshold normally never reaches this
// function at all. This is the second of the two gates, not the only one.
func Suppress[T any](respondents int64, value T) models.Suppressible[T] {
	if respondents < models.MinGroupSize {
		return models.Hidden[T]()
	}
	return models.Visible(value)
}

// MinGroupSizeSQL is the threshold for HAVING clauses, so the number lives in one place.
const MinGroupSizeSQL = models.MinGroupSize

// ---------------------------------------------------------------------------
// PII redaction
// ---------------------------------------------------------------------------

var (
	emailRe = regexp.MustCompile(`[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}.\-]+\.[A-Za-z]{2,}`)

	// Thai mobile/landline in the common written forms: 0812345678, 081-234-5678,
	// 081 234 5678, +66812345678.
	phoneRe = regexp.MustCompile(`(?:\+66|0)[\s\-.]?\d{1,2}[\s\-.]?\d{3}[\s\-.]?\d{3,4}`)

	// 13-digit Thai national ID, with or without the conventional separators.
	nationalIDRe = regexp.MustCompile(`\b\d[\s\-]?\d{4}[\s\-]?\d{5}[\s\-]?\d{2}[\s\-]?\d\b`)

	// Employee/staff codes such as EMP-01234 or "รหัสพนักงาน 01234".
	employeeIDRe = regexp.MustCompile(`(?i)\b(?:emp|staff|no|id)[\s\-_.#]?\d{3,}\b`)
	thaiEmpIDRe  = regexp.MustCompile(`(?:รหัสพนักงาน|รหัสประจำตัว|เลขประจำตัว)[\s:：]*\d{3,}`)

	urlRe = regexp.MustCompile(`(?i)\bhttps?://\S+`)

	// Social handles (@someone) — not @ inside an email, which is redacted first.
	handleRe = regexp.MustCompile(`(?:^|\s)@[\p{L}\p{N}._]{2,}`)

	// Thai honorific + given name, e.g. "คุณสมชาย", "พี่หน่อย", "นายสมศักดิ์".
	//
	// The length cap matters: Thai is written without spaces, so an unbounded match after
	// an honorific swallows the rest of the sentence. Six characters covers most Thai given
	// names and bounds the damage. It will sometimes clip a few characters of the next word
	// — that is the intended trade-off, since leaking the name is the worse failure.
	thaiNameRe = regexp.MustCompile(`(?:คุณ|นาย|นาง|นางสาว|น\.ส\.|พี่|น้อง)[ก-๙]{2,6}`)

	// Latin-script honorific + name, e.g. "Mr. Somchai", "Khun Nok".
	latinNameRe = regexp.MustCompile(`(?i)\b(?:mr|mrs|ms|miss|dr|khun|k)\.?\s+[A-Z][a-z]+`)
)

const redactionMark = "[ถูกปกปิด]"

// redactors run in order; email before handle so an address is not half-eaten by the
// handle rule, and specific ID shapes before the looser name shapes.
var redactors = []*regexp.Regexp{
	urlRe,
	emailRe,
	nationalIDRe,
	thaiEmpIDRe,
	employeeIDRe,
	phoneRe,
	handleRe,
	thaiNameRe,
	latinNameRe,
}

// Redact strips personally identifying fragments from free text. It runs before the text
// is written to the database, so raw answers are never persisted — a display-time filter
// would leave the original sitting in a table for anyone with query access to read.
//
// ponytail: regex-based, which catches the shapes people actually paste (contact details,
// ID numbers, honorific+name) but not an unmarked bare name, and which cannot segment Thai
// precisely — see the length cap on thaiNameRe. Swap in a Thai tokenizer or NER pass in the
// analysis pipeline if either gap matters; the call site here does not change.
func Redact(text string) string {
	out := text
	for _, re := range redactors {
		out = re.ReplaceAllStringFunc(out, func(match string) string {
			// Keep any leading whitespace the pattern needed for context.
			trimmed := strings.TrimLeft(match, " \t\n")
			prefix := match[:len(match)-len(trimmed)]
			return prefix + redactionMark
		})
	}
	return out
}

// LooksIdentifying reports whether a *question* appears to ask for identifying information.
// Used to warn HR in the form builder — mirrors the frontend's looksIdentifying().
func LooksIdentifying(questionText string) bool {
	lowered := strings.ToLower(questionText)
	for _, kw := range identifyingKeywords {
		if strings.Contains(lowered, kw) {
			return true
		}
	}
	return false
}

var identifyingKeywords = []string{
	// Thai
	"ชื่อ", "นามสกุล", "รหัสพนักงาน", "เบอร์โทร", "อีเมล", "ที่อยู่",
	"เลขบัตรประชาชน", "ไลน์ไอดี", "line id",
	// English
	"full name", "your name", "surname", "employee id", "staff id",
	"phone", "mobile", "email", "address", "national id",
}
