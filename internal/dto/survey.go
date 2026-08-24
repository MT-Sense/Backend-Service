package dto

import (
	"fmt"
	"time"

	"github.com/mt-sense/backend-service/internal/models"
)

// SurveyPeriod is a monthly survey window. Under the fixed-question model there is no
// authored "form" to fetch — the client just needs to know which period is open, whether the
// caller already submitted it, and which optional extra questions (see ExtraQuestionCatalog)
// HR turned on for this specific round.
type SurveyPeriod struct {
	ID                    string    `json:"id"`
	Month                 int       `json:"month"`
	Year                  int       `json:"year"`
	OpensAt               time.Time `json:"opensAt"`
	ClosesAt              time.Time `json:"closesAt"`
	IsOpen                bool      `json:"isOpen"`
	AlreadySubmitted      bool      `json:"alreadySubmitted"`
	ResponseCount         int64     `json:"responseCount"`
	EnabledExtraQuestions []string  `json:"enabledExtraQuestions"`
}

func NewSurveyPeriod(p *models.SurveyPeriod, alreadySubmitted bool, responseCount int64) SurveyPeriod {
	now := time.Now()
	extra := p.EnabledExtraQuestions
	if extra == nil {
		extra = []string{}
	}
	return SurveyPeriod{
		ID:                    p.ID,
		Month:                 int(p.Month),
		Year:                  int(p.Year),
		OpensAt:               p.OpensAt,
		ClosesAt:              p.ClosesAt,
		IsOpen:                !now.Before(p.OpensAt) && now.Before(p.ClosesAt),
		AlreadySubmitted:      alreadySubmitted,
		ResponseCount:         responseCount,
		EnabledExtraQuestions: extra,
	}
}

func NewSurveyPeriodList(periods []models.SurveyPeriod, counts map[string]int64) []SurveyPeriod {
	out := make([]SurveyPeriod, 0, len(periods))
	for i := range periods {
		out = append(out, NewSurveyPeriod(&periods[i], false, counts[periods[i].ID]))
	}
	return out
}

// --- fixed catalog of optional extra questions ---
//
// Not a form builder: HR can only toggle these seven fixed questions on/off per round, never
// author new ones. Six reuse the existing topic taxonomy as 1-5 scale questions (so their
// answers can eventually feed the same heatmap/radar dimensions); the seventh is eNPS 0-10.

type ExtraQuestionType string

const (
	ExtraQuestionScale ExtraQuestionType = "scale_1_5"
	ExtraQuestionENPS  ExtraQuestionType = "enps_0_10"
)

type ExtraQuestionDef struct {
	Key   string            `json:"key"`
	Type  ExtraQuestionType `json:"type"`
	Label Localized         `json:"label"`
}

var ExtraQuestionCatalog = []ExtraQuestionDef{
	{Key: "work", Type: ExtraQuestionScale, Label: Localized{TH: "ภาระงานเหมาะสม", EN: "Workload is manageable"}},
	{Key: "team", Type: ExtraQuestionScale, Label: Localized{TH: "ความพึงพอใจต่อทีม", EN: "Satisfaction with team"}},
	{Key: "manager", Type: ExtraQuestionScale, Label: Localized{TH: "ความพึงพอใจต่อหัวหน้างาน", EN: "Satisfaction with manager"}},
	{Key: "compensation", Type: ExtraQuestionScale, Label: Localized{TH: "ความพึงพอใจค่าตอบแทน", EN: "Satisfaction with compensation"}},
	{Key: "growth", Type: ExtraQuestionScale, Label: Localized{TH: "โอกาสเติบโตในสายงาน", EN: "Growth opportunity"}},
	{Key: "benefits", Type: ExtraQuestionScale, Label: Localized{TH: "ความพึงพอใจสวัสดิการ", EN: "Satisfaction with benefits"}},
	{Key: "enps", Type: ExtraQuestionENPS, Label: Localized{TH: "แนะนำที่นี่ให้เพื่อนมาทำงานไหม (0-10)", EN: "How likely to recommend working here (0-10)"}},
}

func ValidExtraQuestionKeys() map[string]ExtraQuestionDef {
	out := make(map[string]ExtraQuestionDef, len(ExtraQuestionCatalog))
	for _, q := range ExtraQuestionCatalog {
		out[q.Key] = q
	}
	return out
}

// ExtraQuestionResult is the HR-only aggregate view of answers to one enabled extra question
// for a period — company-wide average, same "no suppression on whole-company aggregates"
// convention as ENPS/SatisfactionAverage elsewhere in this package (n<5 suppression applies
// to department/position *slices*, not the single company-wide number).
type ExtraQuestionResult struct {
	Key             string            `json:"key"`
	Type            ExtraQuestionType `json:"type"`
	Label           Localized         `json:"label"`
	Average         float64           `json:"average"`
	RespondentCount int64             `json:"respondentCount"`
}

// CreatePeriodRequest opens the next monthly round. ClosesAt defaults to one calendar month
// after OpensAt when omitted (set by the handler, not here).
type CreatePeriodRequest struct {
	Month                 int        `json:"month"`
	Year                  int        `json:"year"`
	OpensAt               *time.Time `json:"opensAt"`
	ClosesAt              *time.Time `json:"closesAt"`
	EnabledExtraQuestions []string   `json:"enabledExtraQuestions"`
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
	catalog := ValidExtraQuestionKeys()
	for _, key := range r.EnabledExtraQuestions {
		if _, ok := catalog[key]; !ok {
			problems = append(problems, fmt.Sprintf("unknown extra question key %q", key))
		}
	}
	return problems
}

// SubmitResponseRequest is the fixed survey payload: one satisfaction score (1-5) plus an
// optional open comment, sent for the currently open period — plus answers to whichever
// extra questions (see ExtraQuestionCatalog) that period has enabled. ExtraAnswers is
// optional field-by-field; the employee can skip any or all of them.
type SubmitResponseRequest struct {
	SatisfactionScore int            `json:"satisfactionScore"`
	CommentText       string         `json:"commentText"`
	OptedInToFeed     bool           `json:"optedInToFeed"`
	Tags              []string       `json:"tags"`
	ExtraAnswers      map[string]int `json:"extraAnswers"`
}

func (r *SubmitResponseRequest) Validate() []string {
	var problems []string
	if r.SatisfactionScore < 1 || r.SatisfactionScore > 5 {
		problems = append(problems, "satisfactionScore must be between 1 and 5")
	}
	catalog := ValidExtraQuestionKeys()
	for key, value := range r.ExtraAnswers {
		def, ok := catalog[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("unknown extra question key %q", key))
			continue
		}
		switch def.Type {
		case ExtraQuestionScale:
			if value < 1 || value > 5 {
				problems = append(problems, fmt.Sprintf("%s must be between 1 and 5", key))
			}
		case ExtraQuestionENPS:
			if value < 0 || value > 10 {
				problems = append(problems, fmt.Sprintf("%s must be between 0 and 10", key))
			}
		}
	}
	return problems
}

// SubmitResponseReceipt confirms the submission without identifying the submitter — there is
// intentionally no user field on this struct.
type SubmitResponseReceipt struct {
	SubmittedAt time.Time `json:"submittedAt"`
}
