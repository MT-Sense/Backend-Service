package dto

import (
	"strings"
	"time"

	"github.com/mt-sense/backend-service/internal/models"
)

// --- requests ---

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r *LoginRequest) Normalize() {
	r.Email = strings.TrimSpace(strings.ToLower(r.Email))
}

func (r *LoginRequest) Validate() []string {
	var problems []string
	if r.Email == "" {
		problems = append(problems, "email is required")
	}
	if r.Password == "" {
		problems = append(problems, "password is required")
	}
	return problems
}

type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type UpdateSettingsRequest struct {
	NotifyNewRound       *bool `json:"notifyNewRound"`
	NotifyMonthlySummary *bool `json:"notifyMonthlySummary"`
}

// Updates returns the column set to write. Only the two notification toggles are
// accepted — role, department and email are not user-editable, and listing the allowed
// columns here means a future model field cannot become writable by accident.
func (r *UpdateSettingsRequest) Updates() map[string]any {
	updates := map[string]any{}
	if r.NotifyNewRound != nil {
		updates["notify_new_round"] = *r.NotifyNewRound
	}
	if r.NotifyMonthlySummary != nil {
		updates["notify_monthly_summary"] = *r.NotifyMonthlySummary
	}
	return updates
}

// --- responses ---

// User mirrors the frontend's CurrentUser. PasswordHash and the raw department/position ids
// have no field here at all, so they cannot leak regardless of what the model gains later.
type User struct {
	ID                   string    `json:"id"`
	Email                string    `json:"email"`
	FullName             string    `json:"fullName"`
	Role                 string    `json:"role"`
	Department           string    `json:"department"`
	Position             string    `json:"position"`
	LastLoginAt          time.Time `json:"lastLoginAt"`
	NotifyNewRound       bool      `json:"notifyNewRound"`
	NotifyMonthlySummary bool      `json:"notifyMonthlySummary"`
}

// NewUser builds the DTO. departmentName/positionName are resolved by the caller, which
// already has the rows loaded.
func NewUser(u *models.User, departmentName, positionName string) User {
	return User{
		ID:                   u.ID,
		Email:                u.Email,
		FullName:             u.FullName,
		Role:                 string(u.Role),
		Department:           departmentName,
		Position:             positionName,
		LastLoginAt:          u.LastLoginAt,
		NotifyNewRound:       u.NotifyNewRound,
		NotifyMonthlySummary: u.NotifyMonthlySummary,
	}
}

type AuthResponse struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	User         User      `json:"user"`
}

// PeriodSubmissionStatus reports, per survey period, only whether the caller submitted —
// never what they answered. It is built from SurveySubmission, which shares no key with
// SurveyResponse.
type PeriodSubmissionStatus struct {
	PeriodID    string     `json:"periodId"`
	Month       int        `json:"month"`
	Year        int        `json:"year"`
	Status      string     `json:"status"` // submitted | not_submitted
	SubmittedAt *time.Time `json:"submittedAt"`
}

func NewSubmissionHistory(periods []models.SurveyPeriod, submissions []models.SurveySubmission) []PeriodSubmissionStatus {
	byPeriod := make(map[string]time.Time, len(submissions))
	for _, s := range submissions {
		byPeriod[s.PeriodID] = s.SubmittedAt
	}

	out := make([]PeriodSubmissionStatus, 0, len(periods))
	for _, p := range periods {
		status := "not_submitted"
		var submittedAt *time.Time
		if t, ok := byPeriod[p.ID]; ok {
			status = "submitted"
			tCopy := t
			submittedAt = &tCopy
		}
		out = append(out, PeriodSubmissionStatus{
			PeriodID:    p.ID,
			Month:       int(p.Month),
			Year:        int(p.Year),
			Status:      status,
			SubmittedAt: submittedAt,
		})
	}
	return out
}
