package dto

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mt-sense/backend-service/internal/models"
)

const (
	MaxSummaryTitleLength = 200
	MaxSummaryBodyLength  = 5000
	MaxAssigneeLength     = 255
)

// SaveSummaryRequest creates or replaces an HR summary. Thai is required; English is
// optional (the UI falls back to Thai). PeriodID is optional and ties the summary to a round.
type SaveSummaryRequest struct {
	PeriodID string    `json:"periodId"`
	Title    Localized `json:"title"`
	Body     Localized `json:"body"`
	Status   string    `json:"status"`
}

func (r *SaveSummaryRequest) Normalize() {
	r.PeriodID = strings.TrimSpace(r.PeriodID)
	r.Title.TH, r.Title.EN = strings.TrimSpace(r.Title.TH), strings.TrimSpace(r.Title.EN)
	r.Body.TH, r.Body.EN = strings.TrimSpace(r.Body.TH), strings.TrimSpace(r.Body.EN)
	r.Status = strings.TrimSpace(r.Status)
	if r.Status == "" {
		r.Status = models.SummaryDraft
	}
}

func (r *SaveSummaryRequest) Validate() []string {
	var problems []string
	if r.Title.TH == "" {
		problems = append(problems, "title (Thai) is required")
	}
	if r.Body.TH == "" {
		problems = append(problems, "body (Thai) is required")
	}
	for _, s := range []string{r.Title.TH, r.Title.EN} {
		if utf8.RuneCountInString(s) > MaxSummaryTitleLength {
			problems = append(problems, "title is too long")
			break
		}
	}
	for _, s := range []string{r.Body.TH, r.Body.EN} {
		if utf8.RuneCountInString(s) > MaxSummaryBodyLength {
			problems = append(problems, "body is too long")
			break
		}
	}
	if r.Status != models.SummaryDraft && r.Status != models.SummaryPublished {
		problems = append(problems, "status must be draft or published")
	}
	return problems
}

// UpdateActionItemRequest edits an action item. Nil fields are left unchanged.
type UpdateActionItemRequest struct {
	Status     *string `json:"status"`
	Assignee   *string `json:"assignee"`
	TargetDate *string `json:"targetDate"`
}

func (r *UpdateActionItemRequest) IsEmpty() bool {
	return r.Status == nil && r.Assignee == nil && r.TargetDate == nil
}

func (r *UpdateActionItemRequest) Validate() []string {
	var problems []string
	if r.Status != nil && *r.Status != models.ActionItemInProgress && *r.Status != models.ActionItemDone {
		problems = append(problems, "status must be in_progress or done")
	}
	if r.Assignee != nil {
		*r.Assignee = strings.TrimSpace(*r.Assignee)
		if utf8.RuneCountInString(*r.Assignee) > MaxAssigneeLength {
			problems = append(problems, "assignee is too long")
		}
	}
	if r.TargetDate != nil {
		*r.TargetDate = strings.TrimSpace(*r.TargetDate)
		if !validDate(*r.TargetDate) {
			problems = append(problems, "targetDate must be YYYY-MM-DD")
		}
	}
	return problems
}

// validDate accepts "" (no date) or a YYYY-MM-DD calendar date.
func validDate(value string) bool {
	if value == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}
