package handlers

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

type AuthHandler struct {
	db     *gorm.DB
	issuer *auth.Issuer
}

func NewAuthHandler(db *gorm.DB, issuer *auth.Issuer) *AuthHandler {
	return &AuthHandler{db: db, issuer: issuer}
}

// Login verifies credentials and mints the token pair. The role comes from the database
// record — the client cannot request or influence it, which is what makes the role badge
// in the UI a display of fact rather than a choice.
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req dto.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var user models.User
	err := h.db.Where("email = ?", req.Email).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Same message and roughly the same work either way, so the response cannot be
		// used to discover which addresses have accounts.
		bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(req.Password))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid email or password")
	}
	if err != nil {
		return err
	}
	if !user.IsActive {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid email or password")
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid email or password")
	}

	user.LastLoginAt = time.Now()
	if err := h.db.Model(&user).Update("last_login_at", user.LastLoginAt).Error; err != nil {
		return err
	}

	return h.issuePair(c, &user)
}

// dummyHash is a valid bcrypt hash of a random string, compared against when no user was
// found so that the timing of a miss resembles the timing of a wrong password.
const dummyHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// Refresh rotates the refresh token: the presented one is revoked and a new pair issued,
// so a stolen token is usable at most once before the real user's next refresh invalidates it.
func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	var req dto.RefreshRequest
	if err := c.BodyParser(&req); err != nil || req.RefreshToken == "" {
		return fiber.NewError(fiber.StatusBadRequest, "refreshToken is required")
	}

	var stored models.RefreshToken
	err := h.db.Where("token_hash = ?", auth.HashToken(req.RefreshToken)).First(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid refresh token")
	}
	if err != nil {
		return err
	}
	if stored.RevokedAt != nil || time.Now().After(stored.ExpiresAt) {
		return fiber.NewError(fiber.StatusUnauthorized, "refresh token expired or revoked")
	}

	var user models.User
	if err := h.db.First(&user, "id = ?", stored.UserID).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "account no longer exists")
	}

	now := time.Now()
	if err := h.db.Model(&stored).Update("revoked_at", &now).Error; err != nil {
		return err
	}

	return h.issuePair(c, &user)
}

// Logout revokes every refresh token for the caller — the "log out of all devices" action.
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	now := time.Now()
	err := h.db.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", middleware.UserID(c)).
		Update("revoked_at", &now).Error
	if err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *AuthHandler) issuePair(c *fiber.Ctx, user *models.User) error {
	resp, err := issueTokenPair(h.db, h.issuer, user)
	if err != nil {
		return err
	}
	return c.JSON(resp)
}

// issueTokenPair mints an access+refresh token pair for user and resolves their department/
// position display names — shared by AuthHandler (login/refresh) and OnboardingHandler
// (signup/employee registration) so token issuance has exactly one implementation.
func issueTokenPair(db *gorm.DB, issuer *auth.Issuer, user *models.User) (dto.AuthResponse, error) {
	access, expiresAt, err := issuer.AccessToken(user.ID, user.OrgID, user.Role)
	if err != nil {
		return dto.AuthResponse{}, err
	}
	refresh, hash, refreshExpiry, err := issuer.RefreshToken()
	if err != nil {
		return dto.AuthResponse{}, err
	}
	err = db.Create(&models.RefreshToken{
		UserID:    user.ID,
		TokenHash: hash,
		ExpiresAt: refreshExpiry,
	}).Error
	if err != nil {
		return dto.AuthResponse{}, err
	}

	deptName := departmentName(db, user.DepartmentID)
	posName := positionName(db, user.PositionID)

	return dto.AuthResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    expiresAt,
		User:         dto.NewUser(user, deptName, posName),
	}, nil
}

func departmentName(db *gorm.DB, departmentID *string) string {
	if departmentID == nil {
		return ""
	}
	var department models.Department
	if db.First(&department, "id = ?", *departmentID).Error != nil {
		return ""
	}
	return department.Name
}

func positionName(db *gorm.DB, positionID *string) string {
	if positionID == nil {
		return ""
	}
	var position models.Position
	if db.First(&position, "id = ?", *positionID).Error != nil {
		return ""
	}
	return position.Name
}

// Me returns the signed-in user for the settings screen.
func (h *AuthHandler) Me(c *fiber.Ctx) error {
	var user models.User
	if err := h.db.First(&user, "id = ?", middleware.UserID(c)).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}
	return c.JSON(dto.NewUser(&user, departmentName(h.db, user.DepartmentID), positionName(h.db, user.PositionID)))
}

// UpdateMe changes the caller's own notification preferences and nothing else.
func (h *AuthHandler) UpdateMe(c *fiber.Ctx) error {
	var req dto.UpdateSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}

	updates := req.Updates()
	if len(updates) == 0 {
		return fiber.NewError(fiber.StatusBadRequest, "no supported fields to update")
	}

	err := h.db.Model(&models.User{}).Where("id = ?", middleware.UserID(c)).Updates(updates).Error
	if err != nil {
		return err
	}
	return h.Me(c)
}

// SubmissionHistory reports only whether the caller submitted each survey period, never what
// they answered. It reads survey_submissions, which shares no key with survey_responses.
func (h *AuthHandler) SubmissionHistory(c *fiber.Ctx) error {
	var periods []models.SurveyPeriod
	if err := h.db.Where("org_id = ?", middleware.OrgID(c)).Order("year DESC, month DESC").Find(&periods).Error; err != nil {
		return err
	}

	var submissions []models.SurveySubmission
	err := h.db.Where("user_id = ?", middleware.UserID(c)).Find(&submissions).Error
	if err != nil {
		return err
	}

	return c.JSON(dto.NewSubmissionHistory(periods, submissions))
}
