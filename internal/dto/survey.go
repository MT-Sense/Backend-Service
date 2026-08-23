package dto

import (
	"time"

	"github.com/mt-sense/backend-service/internal/models"
)

// SurveyPeriod is a monthly survey window. Under the fixed-question model there is no
// authored "form" to fetch — the client just needs to know which period is open and
// whether the caller already submitted it.
type SurveyPeriod struct {
	ID               string    `json:"id"`
	Month            int       `json:"month"`
	Year             int       `json:"year"`
	OpensAt          time.Time `json:"opensAt"`
	ClosesAt         time.Time `json:"closesAt"`
	IsOpen           bool      `json:"isOpen"`
	AlreadySubmitted bool      `json:"alreadySubmitted"`
	ResponseCount    int64     `json:"responseCount"`
}

func NewSurveyPeriod(p *models.SurveyPeriod, alreadySubmitted bool, responseCount int64) SurveyPeriod {
	now := time.Now()
	return SurveyPeriod{
		ID:               p.ID,
		Month:            int(p.Month),
		Year:             int(p.Year),
		OpensAt:          p.OpensAt,
		ClosesAt:         p.ClosesAt,
		IsOpen:           !now.Before(p.OpensAt) && now.Before(p.ClosesAt),
		AlreadySubmitted: alreadySubmitted,
		ResponseCount:    responseCount,
	}
}

func NewSurveyPeriodList(periods []models.SurveyPeriod, counts map[string]int64) []SurveyPeriod {
	out := make([]SurveyPeriod, 0, len(periods))
	for i := range periods {
		out = append(out, NewSurveyPeriod(&periods[i], false, counts[periods[i].ID]))
	}
	return out
}

// CreatePeriodRequest opens the next monthly round. ClosesAt defaults to one calendar month
// after OpensAt when omitted (set by the handler, not here).
type CreatePeriodRequest struct {
	Month    int        `json:"month"`
	Year     int        `json:"year"`
	OpensAt  *time.Time `json:"opensAt"`
	ClosesAt *time.Time `json:"closesAt"`
}

func (r *CreatePeriodRequest) Validate() []string {
	var problems []string
	if r.Month < 1 || r.Month > 12 {
		problems = append(problems, "month must be between 1 and 12")
	}
	if r.Year < 2000 || r.Year > 2100 {
		problems = append(problems, "year is out of range")
	}
	if r.OpensAt != nil && r.ClosesAt != nil && !r.ClosesAt.After(*r.OpensAt) {
		problems = append(problems, "closesAt must be after opensAt")
	}
	return problems
}

// SubmitResponseRequest is the fixed 2-field survey payload the schema models: one
// satisfaction score (1-5) plus an optional open comment, sent for the currently open period.
type SubmitResponseRequest struct {
	SatisfactionScore int      `json:"satisfactionScore"`
	CommentText       string   `json:"commentText"`
	OptedInToFeed     bool     `json:"optedInToFeed"`
	Tags              []string `json:"tags"`
}

func (r *SubmitResponseRequest) Validate() []string {
	var problems []string
	if r.SatisfactionScore < 1 || r.SatisfactionScore > 5 {
		problems = append(problems, "satisfactionScore must be between 1 and 5")
	}
	return problems
}

// SubmitResponseReceipt confirms the submission without identifying the submitter — there is
// intentionally no user field on this struct.
type SubmitResponseReceipt struct {
	SubmittedAt time.Time `json:"submittedAt"`
}
