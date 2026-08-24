package handlers

import (
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// OnboardingHandler implements self-service company signup and employee join-by-code.
// Unlike the other handlers it never carries an org id at all — every method here either
// creates the org (Signup) or resolves it fresh from a join code / the caller's JWT.
type OnboardingHandler struct {
	db     *gorm.DB
	issuer *auth.Issuer
}

func NewOnboardingHandler(db *gorm.DB, issuer *auth.Issuer) *OnboardingHandler {
	return &OnboardingHandler{db: db, issuer: issuer}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const maxJoinCodeAttempts = 5

// Signup creates a new organization and its first user (role admin/HR) in one transaction,
// then signs them in immediately — no email verification gate this round (see plan §10).
func (h *OnboardingHandler) Signup(c *fiber.Ctx) error {
	var req dto.SignupRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	slug := req.Slug()
	if problems := dto.ValidateSlug(slug); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	var org models.Organization
	var admin models.User

	for attempt := 0; ; attempt++ {
		code, err := auth.JoinCode()
		if err != nil {
			return err
		}
		org = models.Organization{
			ID:   uuid.NewString(),
			Name: req.CompanyName,
			Slug: slug,
			// CollectDepartment starts off: a brand-new org has zero departments (there is
			// no department-management UI yet — see WIKI-Backend.md), so defaulting this on
			// would show joining employees an empty, unusable dropdown. HR can turn it on
			// from Settings once departments exist for the org.
			JoinCode:          code,
			CollectDepartment: false,
			CollectTenure:     false,
		}
		admin = models.User{
			ID:           uuid.NewString(),
			OrgID:        org.ID,
			Email:        req.AdminEmail,
			PasswordHash: string(passwordHash),
			Role:         models.RoleAdmin,
			IsActive:     true,
			FullName:     req.AdminFullName,
		}

		txErr := h.db.Transaction(func(tx *gorm.DB) error {
			// Select("*") forces every field into the INSERT even at its Go zero value —
			// without it, GORM omits zero-valued fields that have a `default` gorm tag
			// (CollectDepartment's default is true) and lets Postgres apply the column
			// default instead, silently turning our explicit false back into true.
			if err := tx.Select("*").Create(&org).Error; err != nil {
				return err
			}
			return tx.Create(&admin).Error
		})
		if txErr == nil {
			break
		}
		if isUniqueViolation(txErr) {
			// Could be the join code (retry with a fresh one) or the slug/email (a real
			// conflict — report it). Distinguish by re-checking which one already exists.
			var slugCount, emailCount int64
			h.db.Model(&models.Organization{}).Where("slug = ?", slug).Count(&slugCount)
			h.db.Model(&models.User{}).Where("email = ?", req.AdminEmail).Count(&emailCount)
			if slugCount > 0 {
				return fiber.NewError(fiber.StatusConflict, "companySlug is already taken")
			}
			if emailCount > 0 {
				return fiber.NewError(fiber.StatusConflict, "adminEmail is already registered")
			}
			if attempt+1 >= maxJoinCodeAttempts {
				return fiber.NewError(fiber.StatusInternalServerError, "could not allocate a join code, please try again")
			}
			continue
		}
		return txErr
	}

	resp, err := issueTokenPair(h.db, h.issuer, &admin)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.SignupResponse{AuthResponse: resp, JoinCode: org.JoinCode})
}

// CheckJoinCode is the public pre-check a joining employee hits first: does this code
// resolve to a company, and does the form need a company password / department / tenure
// field. Rate-limited at the router (router.go) — this is the guessing surface for the
// 6-char code.
func (h *OnboardingHandler) CheckJoinCode(c *fiber.Ctx) error {
	var req dto.JoinCodeCheckRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var org models.Organization
	if err := h.db.Where("join_code = ?", req.Code).First(&org).Error; err != nil {
		return c.JSON(dto.JoinCodeCheckResponse{Valid: false})
	}

	var departmentOptions []dto.JoinCodeOption
	if org.CollectDepartment {
		var departments []models.Department
		if err := h.db.Where("org_id = ?", org.ID).Order("name").Find(&departments).Error; err != nil {
			return err
		}
		departmentOptions = make([]dto.JoinCodeOption, 0, len(departments))
		for _, d := range departments {
			departmentOptions = append(departmentOptions, dto.JoinCodeOption{ID: d.ID, Name: d.Name})
		}
	}

	return c.JSON(dto.JoinCodeCheckResponse{
		Valid:                   true,
		CompanyName:             org.Name,
		RequiresCompanyPassword: org.CompanyPasswordHash != nil,
		CollectDepartment:       org.CollectDepartment,
		CollectTenure:           org.CollectTenure,
		Departments:             departmentOptions,
	})
}

// dummyCompanyHash lets CheckCompanyPassword and RegisterEmployee run a bcrypt compare even
// when the org has no company password set (or doesn't exist), so response timing doesn't
// leak which join codes are real. Same technique as AuthHandler.Login's dummyHash.
const dummyCompanyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// CheckCompanyPassword is a UX pre-validation step only — RegisterEmployee re-verifies the
// company password server-side before actually creating the account, since there is no
// pre-auth session to carry a "password already verified" flag between requests.
func (h *OnboardingHandler) CheckCompanyPassword(c *fiber.Ctx) error {
	var req dto.CompanyPasswordCheckRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var org models.Organization
	err := h.db.Where("join_code = ?", req.Code).First(&org).Error
	if err != nil || org.CompanyPasswordHash == nil {
		bcrypt.CompareHashAndPassword([]byte(dummyCompanyHash), []byte(req.CompanyPassword))
		return c.JSON(dto.CompanyPasswordCheckResponse{Valid: false})
	}

	valid := bcrypt.CompareHashAndPassword([]byte(*org.CompanyPasswordHash), []byte(req.CompanyPassword)) == nil
	return c.JSON(dto.CompanyPasswordCheckResponse{Valid: valid})
}

// RegisterEmployee creates an employee account under the org identified by the join code.
// Re-verifies the company password (if set) and re-resolves org toggles here rather than
// trusting the earlier check calls, since those were UX pre-validation, not a session.
func (h *OnboardingHandler) RegisterEmployee(c *fiber.Ctx) error {
	var req dto.EmployeeRegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var org models.Organization
	if err := h.db.Where("join_code = ?", req.Code).First(&org).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "invalid join code")
	}

	if org.CompanyPasswordHash != nil {
		provided := ""
		if req.CompanyPassword != nil {
			provided = *req.CompanyPassword
		}
		if bcrypt.CompareHashAndPassword([]byte(*org.CompanyPasswordHash), []byte(provided)) != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid company password")
		}
	}

	var problems []string
	var departmentID *string
	if org.CollectDepartment {
		if req.DepartmentID == nil || *req.DepartmentID == "" {
			problems = append(problems, "departmentId is required")
		} else {
			var dept models.Department
			if err := h.db.Where("id = ? AND org_id = ?", *req.DepartmentID, org.ID).First(&dept).Error; err != nil {
				problems = append(problems, "departmentId is not a department of this organization")
			} else {
				departmentID = &dept.ID
			}
		}
	}
	var tenureBucket *string
	if org.CollectTenure {
		if req.TenureBucket == nil || !dto.IsValidTenureBucket(*req.TenureBucket) {
			problems = append(problems, "tenureBucket is required and must be a valid bucket")
		} else {
			tenureBucket = req.TenureBucket
		}
	}
	if len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	// Position is free text (unlike department, not picked from an existing list) — resolve
	// or create it within the org, matching how positions are org-wide but unrestricted.
	var position models.Position
	err = h.db.Where("org_id = ? AND name = ?", org.ID, req.Position).First(&position).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		position = models.Position{ID: uuid.NewString(), OrgID: org.ID, Name: req.Position}
		if err := h.db.Create(&position).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	user := models.User{
		ID:           uuid.NewString(),
		OrgID:        org.ID,
		Email:        req.Email,
		PasswordHash: string(passwordHash),
		Role:         models.RoleEmployee,
		DepartmentID: departmentID,
		PositionID:   &position.ID,
		IsActive:     true,
		FullName:     fmt.Sprintf("%s %s", req.FirstName, req.LastName),
		TenureBucket: tenureBucket,
	}
	if err := h.db.Create(&user).Error; err != nil {
		if isUniqueViolation(err) {
			return fiber.NewError(fiber.StatusConflict, "email is already registered")
		}
		return err
	}

	resp, err := issueTokenPair(h.db, h.issuer, &user)
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(resp)
}

// GetJoinCode / RegenerateJoinCode / GetOrgSettings / UpdateOrgSettings are admin-only,
// authenticated — org comes from the caller's JWT via middleware.OrgID, same as every other
// admin-only handler in this codebase.

func (h *OnboardingHandler) GetJoinCode(c *fiber.Ctx) error {
	var org models.Organization
	if err := h.db.First(&org, "id = ?", middleware.OrgID(c)).Error; err != nil {
		return err
	}
	return c.JSON(dto.OrgJoinCodeResponse{JoinCode: org.JoinCode})
}

func (h *OnboardingHandler) RegenerateJoinCode(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	for attempt := 0; ; attempt++ {
		code, err := auth.JoinCode()
		if err != nil {
			return err
		}
		err = h.db.Model(&models.Organization{}).Where("id = ?", orgID).Update("join_code", code).Error
		if err == nil {
			return c.JSON(dto.OrgJoinCodeResponse{JoinCode: code})
		}
		if isUniqueViolation(err) && attempt+1 < maxJoinCodeAttempts {
			continue
		}
		return err
	}
}

func (h *OnboardingHandler) GetOrgSettings(c *fiber.Ctx) error {
	var org models.Organization
	if err := h.db.First(&org, "id = ?", middleware.OrgID(c)).Error; err != nil {
		return err
	}
	return c.JSON(dto.OrgSettingsResponse{
		CompanyPasswordSet: org.CompanyPasswordHash != nil,
		CollectDepartment:  org.CollectDepartment,
		CollectTenure:      org.CollectTenure,
	})
}

func (h *OnboardingHandler) UpdateOrgSettings(c *fiber.Ctx) error {
	var req dto.UpdateOrgSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}

	updates := map[string]any{}
	if req.CompanyPassword != nil {
		if *req.CompanyPassword == "" {
			updates["company_password_hash"] = nil
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(*req.CompanyPassword), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			updates["company_password_hash"] = string(hash)
		}
	}
	if req.CollectDepartment != nil {
		updates["collect_department"] = *req.CollectDepartment
	}
	if req.CollectTenure != nil {
		updates["collect_tenure"] = *req.CollectTenure
	}
	if len(updates) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "no supported fields to update")
	}

	orgID := middleware.OrgID(c)
	if err := h.db.Model(&models.Organization{}).Where("id = ?", orgID).Updates(updates).Error; err != nil {
		return err
	}
	return h.GetOrgSettings(c)
}
