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

// User mirrors the frontend's CurrentUser. PasswordHash and the raw DepartmentID have no
// field here at all, so they cannot leak regardless of what the model gains later.
type User struct {
	ID                   string    `json:"id"`
	Email                string    `json:"email"`
	FullName             string    `json:"fullName"`
	Role                 string    `json:"role"`
	Department           string    `json:"department"`
	LastLoginAt          time.Time `json:"lastLoginAt"`
	NotifyNewRound       bool      `json:"notifyNewRound"`
	NotifyMonthlySummary bool      `json:"notifyMonthlySummary"`
}

// NewUser builds the DTO. departmentName is resolved by the caller, which already has the
// department loaded; passing "" falls back to the raw id.
func NewUser(u *models.User, departmentName string) User {
	if departmentName == "" {
		departmentName = u.DepartmentID
	}
	return User{
		ID:                   u.ID,
		Email:                u.Email,
		FullName:             u.FullName,
		Role:                 string(u.Role),
		Department:           departmentName,
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

// SubmissionLogEntry mirrors AuditSubmissionLogEntry: whether a survey was submitted, and
// nothing whatsoever about what was answered.
type SubmissionLogEntry struct {
	UserID      string     `json:"userId"`
	SurveyID    string     `json:"surveyId"`
	Status      string     `json:"status"`
	SubmittedAt *time.Time `json:"submittedAt"`
}

func NewSubmissionLog(entries []models.AuditSubmissionLog) []SubmissionLogEntry {
	out := make([]SubmissionLogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, SubmissionLogEntry{
			UserID:      e.UserID,
			SurveyID:    e.SurveyID,
			Status:      e.Status,
			SubmittedAt: e.SubmittedAt,
		})
	}
	return out
}
