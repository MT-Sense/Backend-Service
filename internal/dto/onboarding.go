package dto

import (
	"net/mail"
	"strings"
)

// reservedSlugs blocks new orgs from colliding with the seeded demo org or common
// infrastructure paths.
var reservedSlugs = map[string]bool{
	"mt-sense": true,
	"admin":    true,
	"api":      true,
	"www":      true,
}

// --- signup (HR creates a new company) ---

type SignupRequest struct {
	CompanyName   string  `json:"companyName"`
	CompanySlug   *string `json:"companySlug"`
	AdminFullName string  `json:"adminFullName"`
	AdminEmail    string  `json:"adminEmail"`
	AdminPassword string  `json:"adminPassword"`
}

func (r *SignupRequest) Normalize() {
	r.CompanyName = strings.TrimSpace(r.CompanyName)
	r.AdminFullName = strings.TrimSpace(r.AdminFullName)
	r.AdminEmail = strings.TrimSpace(strings.ToLower(r.AdminEmail))
	if r.CompanySlug != nil {
		s := strings.TrimSpace(strings.ToLower(*r.CompanySlug))
		r.CompanySlug = &s
	}
}

func (r *SignupRequest) Validate() []string {
	var problems []string
	if r.CompanyName == "" {
		problems = append(problems, "companyName is required")
	}
	if r.AdminFullName == "" {
		problems = append(problems, "adminFullName is required")
	}
	if r.AdminEmail == "" {
		problems = append(problems, "adminEmail is required")
	} else if _, err := mail.ParseAddress(r.AdminEmail); err != nil {
		problems = append(problems, "adminEmail is not a valid email address")
	}
	if len(r.AdminPassword) < 8 {
		problems = append(problems, "adminPassword must be at least 8 characters")
	}
	if r.CompanySlug != nil && *r.CompanySlug != "" {
		problems = append(problems, ValidateSlug(*r.CompanySlug)...)
	}
	return problems
}

// Slug returns the company slug to use: the explicit CompanySlug if provided, otherwise one
// derived from CompanyName. Call after Validate() — an explicit CompanySlug is already known
// valid at that point; a derived one still needs ValidateSlug (the handler re-checks it,
// since a company name can slugify into something reserved even though the name itself
// wasn't rejected).
func (r *SignupRequest) Slug() string {
	if r.CompanySlug != nil && *r.CompanySlug != "" {
		return *r.CompanySlug
	}
	return slugify(r.CompanyName)
}

// ValidateSlug checks charset and the reserved-word list shared by explicit and
// name-derived slugs alike.
func ValidateSlug(slug string) []string {
	if !isValidSlug(slug) {
		return []string{"companySlug may only contain lowercase letters, numbers and hyphens"}
	}
	if reservedSlugs[slug] {
		return []string{"companySlug is reserved, please choose another"}
	}
	return nil
}

func isValidSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return false
		}
	}
	return true
}

// slugify lowercases a company name and collapses anything that isn't a letter/digit into a
// single hyphen, trimming leading/trailing hyphens — a best-effort default so most HR users
// never need to type a slug themselves.
func slugify(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// SignupResponse carries the new HR admin's session (same shape as login) plus the
// company's join code, shown inline once so the UI doesn't need a second round trip.
type SignupResponse struct {
	AuthResponse
	JoinCode string `json:"joinCode"`
}

// --- join code check (public, pre-registration) ---

type JoinCodeCheckRequest struct {
	Code string `json:"code"`
}

func (r *JoinCodeCheckRequest) Normalize() {
	r.Code = strings.ToUpper(strings.TrimSpace(r.Code))
}

func (r *JoinCodeCheckRequest) Validate() []string {
	if r.Code == "" {
		return []string{"code is required"}
	}
	return nil
}

// JoinCodeOption is a minimal, public-safe department reference — just enough for the
// registration form's dropdown, no respondent counts or anything else org-internal.
type JoinCodeOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type JoinCodeCheckResponse struct {
	Valid                   bool             `json:"valid"`
	CompanyName             string           `json:"companyName,omitempty"`
	RequiresCompanyPassword bool             `json:"requiresCompanyPassword"`
	CollectDepartment       bool             `json:"collectDepartment"`
	CollectTenure           bool             `json:"collectTenure"`
	Departments             []JoinCodeOption `json:"departments,omitempty"`
}

// --- company password check (public, pre-registration) ---

type CompanyPasswordCheckRequest struct {
	Code            string `json:"code"`
	CompanyPassword string `json:"companyPassword"`
}

func (r *CompanyPasswordCheckRequest) Normalize() {
	r.Code = strings.ToUpper(strings.TrimSpace(r.Code))
}

func (r *CompanyPasswordCheckRequest) Validate() []string {
	var problems []string
	if r.Code == "" {
		problems = append(problems, "code is required")
	}
	if r.CompanyPassword == "" {
		problems = append(problems, "companyPassword is required")
	}
	return problems
}

type CompanyPasswordCheckResponse struct {
	Valid bool `json:"valid"`
}

// --- employee registration (public) ---

type EmployeeRegisterRequest struct {
	Code            string  `json:"code"`
	CompanyPassword *string `json:"companyPassword"`
	FirstName       string  `json:"firstName"`
	LastName        string  `json:"lastName"`
	Position        string  `json:"position"`
	Email           string  `json:"email"`
	Password        string  `json:"password"`
	DepartmentID    *string `json:"departmentId"`
	TenureBucket    *string `json:"tenureBucket"`
}

func (r *EmployeeRegisterRequest) Normalize() {
	r.Code = strings.ToUpper(strings.TrimSpace(r.Code))
	r.FirstName = strings.TrimSpace(r.FirstName)
	r.LastName = strings.TrimSpace(r.LastName)
	r.Position = strings.TrimSpace(r.Position)
	r.Email = strings.TrimSpace(strings.ToLower(r.Email))
}

// Validate checks baseline shape only. Whether DepartmentID/TenureBucket are required
// depends on the org's CollectDepartment/CollectTenure toggles, which requires a DB lookup —
// that conditional check happens in the handler, after the org has been resolved.
func (r *EmployeeRegisterRequest) Validate() []string {
	var problems []string
	if r.Code == "" {
		problems = append(problems, "code is required")
	}
	if r.FirstName == "" {
		problems = append(problems, "firstName is required")
	}
	if r.LastName == "" {
		problems = append(problems, "lastName is required")
	}
	if r.Position == "" {
		problems = append(problems, "position is required")
	}
	if r.Email == "" {
		problems = append(problems, "email is required")
	} else if _, err := mail.ParseAddress(r.Email); err != nil {
		problems = append(problems, "email is not a valid email address")
	}
	if len(r.Password) < 8 {
		problems = append(problems, "password must be at least 8 characters")
	}
	return problems
}

var validTenureBuckets = map[string]bool{
	"under_1y": true,
	"1_3y":     true,
	"3_5y":     true,
	"5y_plus":  true,
}

func IsValidTenureBucket(bucket string) bool {
	return validTenureBuckets[bucket]
}

// --- org settings (admin only) ---

type OrgJoinCodeResponse struct {
	JoinCode string `json:"joinCode"`
}

type OrgSettingsResponse struct {
	CompanyPasswordSet bool `json:"companyPasswordSet"`
	CollectDepartment  bool `json:"collectDepartment"`
	CollectTenure      bool `json:"collectTenure"`
}

// UpdateOrgSettingsRequest: CompanyPassword nil means "leave unchanged", empty string means
// "clear the gate", non-empty means "set/replace it".
type UpdateOrgSettingsRequest struct {
	CompanyPassword   *string `json:"companyPassword"`
	CollectDepartment *bool   `json:"collectDepartment"`
	CollectTenure     *bool   `json:"collectTenure"`
}
