package dto

import (
	"strings"
	"unicode/utf8"

	"github.com/mt-sense/backend-service/internal/models"
)

const (
	MaxSummaryRecipients = 20
	MaxSummaryNoteLength = 1000
)

// EmailStatus tells the settings screen whether SMTP is set up and how the queue looks.
type EmailStatus struct {
	Configured bool   `json:"configured"`
	From       string `json:"from"`
	Pending    int64  `json:"pending"`
	Failed     int64  `json:"failed"`
}

// EmailRecipient is a leader (admin/executive) who may receive a dashboard summary.
type EmailRecipient struct {
	ID       string      `json:"id"`
	FullName string      `json:"fullName"`
	Email    string      `json:"email"`
	Role     models.Role `json:"role"`
}

func NewEmailRecipients(users []models.User) []EmailRecipient {
	out := make([]EmailRecipient, 0, len(users))
	for _, u := range users {
		out = append(out, EmailRecipient{ID: u.ID, FullName: u.FullName, Email: u.Email, Role: u.Role})
	}
	return out
}

// SendSummaryRequest emails the org-wide summary of one period. Recipients are user ids,
// never free-form addresses, so a summary cannot leave the organization.
type SendSummaryRequest struct {
	PeriodID     string   `json:"periodId"`
	RecipientIDs []string `json:"recipientIds"`
	Note         string   `json:"note"`
}

// Normalize trims the note and drops blank/duplicate recipient ids.
func (r *SendSummaryRequest) Normalize() {
	r.PeriodID = strings.TrimSpace(r.PeriodID)
	r.Note = strings.TrimSpace(r.Note)
	seen := make(map[string]bool, len(r.RecipientIDs))
	ids := r.RecipientIDs[:0]
	for _, id := range r.RecipientIDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	r.RecipientIDs = ids
}

func (r *SendSummaryRequest) Validate() []string {
	var problems []string
	if len(r.RecipientIDs) == 0 {
		problems = append(problems, "choose at least one recipient")
	}
	if len(r.RecipientIDs) > MaxSummaryRecipients {
		problems = append(problems, "too many recipients")
	}
	if utf8.RuneCountInString(r.Note) > MaxSummaryNoteLength {
		problems = append(problems, "note is too long")
	}
	return problems
}

type SendSummaryReceipt struct {
	Queued int `json:"queued"`
}
