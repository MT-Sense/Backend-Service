package dto

import (
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mt-sense/backend-service/internal/models"
)

const (
	MinPasswordLength = 8
	MaxPasswordLength = 72 // bcrypt ignores bytes past 72
	MaxFullNameLength = 100
	MaxPositionLength = 255
)

// ManagedUser is HR's view of an account: profile and access only. It deliberately has no
// field about surveys (submitted or not, or what was answered).
type ManagedUser struct {
	ID             string      `json:"id"`
	FullName       string      `json:"fullName"`
	Email          string      `json:"email"`
	Role           models.Role `json:"role"`
	DepartmentID   string      `json:"departmentId"`
	DepartmentName string      `json:"departmentName"`
	PositionID     string      `json:"positionId"`
	PositionName   string      `json:"positionName"`
	IsActive       bool        `json:"isActive"`
	CreatedAt      time.Time   `json:"createdAt"`
	// LastLoginAt is null for accounts that never signed in.
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

func NewManagedUser(u *models.User, departments, positions map[string]string) ManagedUser {
	out := ManagedUser{
		ID: u.ID, FullName: u.FullName, Email: u.Email, Role: u.Role,
		IsActive: u.IsActive, CreatedAt: u.CreatedAt,
	}
	if u.DepartmentID != nil {
		out.DepartmentID = *u.DepartmentID
		out.DepartmentName = departments[*u.DepartmentID]
	}
	if u.PositionID != nil {
		out.PositionID = *u.PositionID
		out.PositionName = positions[*u.PositionID]
	}
	if !u.LastLoginAt.IsZero() {
		last := u.LastLoginAt
		out.LastLoginAt = &last
	}
	return out
}

func validRole(role models.Role) bool {
	return role == models.RoleEmployee || role == models.RoleExecutive || role == models.RoleAdmin
}

func validatePassword(password string) []string {
	if len(password) < MinPasswordLength {
		return []string{"password must be at least 8 characters"}
	}
	if len(password) > MaxPasswordLength {
		return []string{"password must be at most 72 bytes"}
	}
	return nil
}

func validateEmail(email string) []string {
	if email == "" {
		return []string{"email is required"}
	}
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		return []string{"email is not a valid email address"}
	}
	return nil
}

// CreateUserRequest is HR adding an account directly, with a temporary password HR passes
// on to the person (they can change it in Settings).
type CreateUserRequest struct {
	FullName     string      `json:"fullName"`
	Email        string      `json:"email"`
	Role         models.Role `json:"role"`
	DepartmentID string      `json:"departmentId"`
	Position     string      `json:"position"`
	Password     string      `json:"password"`
}

func (r *CreateUserRequest) Normalize() {
	r.FullName = strings.TrimSpace(r.FullName)
	r.Email = strings.ToLower(strings.TrimSpace(r.Email))
	r.DepartmentID = strings.TrimSpace(r.DepartmentID)
	r.Position = strings.TrimSpace(r.Position)
	if r.Role == "" {
		r.Role = models.RoleEmployee
	}
}

func (r *CreateUserRequest) Validate() []string {
	var problems []string
	if r.FullName == "" {
		problems = append(problems, "fullName is required")
	} else if utf8.RuneCountInString(r.FullName) > MaxFullNameLength {
		problems = append(problems, "fullName is too long")
	}
	problems = append(problems, validateEmail(r.Email)...)
	if !validRole(r.Role) {
		problems = append(problems, "role must be employee, executive or admin")
	}
	if utf8.RuneCountInString(r.Position) > MaxPositionLength {
		problems = append(problems, "position is too long")
	}
	return append(problems, validatePassword(r.Password)...)
}

// UpdateUserRequest edits an account. Nil fields are unchanged; for DepartmentID and
// Position an empty string clears the value.
type UpdateUserRequest struct {
	FullName     *string      `json:"fullName"`
	Email        *string      `json:"email"`
	Role         *models.Role `json:"role"`
	DepartmentID *string      `json:"departmentId"`
	Position     *string      `json:"position"`
	IsActive     *bool        `json:"isActive"`
}

func (r *UpdateUserRequest) Normalize() {
	trim := func(s *string) {
		if s != nil {
			*s = strings.TrimSpace(*s)
		}
	}
	trim(r.FullName)
	trim(r.DepartmentID)
	trim(r.Position)
	if r.Email != nil {
		*r.Email = strings.ToLower(strings.TrimSpace(*r.Email))
	}
}

func (r *UpdateUserRequest) IsEmpty() bool {
	return r.FullName == nil && r.Email == nil && r.Role == nil && r.DepartmentID == nil && r.Position == nil && r.IsActive == nil
}

func (r *UpdateUserRequest) Validate() []string {
	var problems []string
	if r.FullName != nil {
		if *r.FullName == "" {
			problems = append(problems, "fullName is required")
		} else if utf8.RuneCountInString(*r.FullName) > MaxFullNameLength {
			problems = append(problems, "fullName is too long")
		}
	}
	if r.Email != nil {
		problems = append(problems, validateEmail(*r.Email)...)
	}
	if r.Role != nil && !validRole(*r.Role) {
		problems = append(problems, "role must be employee, executive or admin")
	}
	if r.Position != nil && utf8.RuneCountInString(*r.Position) > MaxPositionLength {
		problems = append(problems, "position is too long")
	}
	return problems
}

// SetPasswordRequest is HR setting a temporary password for someone.
type SetPasswordRequest struct {
	Password string `json:"password"`
}

func (r *SetPasswordRequest) Validate() []string { return validatePassword(r.Password) }

// ChangePasswordRequest is a signed-in user changing their own password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (r *ChangePasswordRequest) Validate() []string {
	var problems []string
	if r.CurrentPassword == "" {
		problems = append(problems, "currentPassword is required")
	}
	problems = append(problems, validatePassword(r.NewPassword)...)
	if r.CurrentPassword != "" && r.CurrentPassword == r.NewPassword {
		problems = append(problems, "new password must differ from the current one")
	}
	return problems
}

type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

func (r *ForgotPasswordRequest) Normalize() { r.Email = strings.ToLower(strings.TrimSpace(r.Email)) }

type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

func (r *ResetPasswordRequest) Validate() []string {
	var problems []string
	if strings.TrimSpace(r.Token) == "" {
		problems = append(problems, "token is required")
	}
	return append(problems, validatePassword(r.NewPassword)...)
}
