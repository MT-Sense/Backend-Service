package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/mailer"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// passwordResetTTL is how long an emailed reset link works.
const passwordResetTTL = time.Hour

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// revokeSessions ends every refresh token of a user, so a password change, role change or
// deactivation takes effect at their next refresh (access tokens expire within minutes).
func revokeSessions(tx *gorm.DB, userID string) error {
	now := time.Now()
	return tx.Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", &now).Error
}

// startPasswordReset issues a single-use reset token for user and queues the email, in one
// transaction. Earlier unused tokens are invalidated so only the newest link works.
func startPasswordReset(db *gorm.DB, mail *mailer.Service, user models.User, requestedByHR bool) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expiresAt := time.Now().Add(passwordResetTTL)

	err := db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&models.PasswordResetToken{}).
			Where("user_id = ? AND used_at IS NULL", user.ID).
			Update("used_at", &now).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.PasswordResetToken{
			UserID: user.ID, TokenHash: auth.HashToken(token), ExpiresAt: expiresAt,
		}).Error; err != nil {
			return err
		}
		return mail.EnqueuePasswordReset(tx, user, token, expiresAt, requestedByHR)
	})
	if err == nil {
		mail.Notify()
	}
	return err
}

// ChangePassword lets a signed-in user replace their own password. Other sessions are
// signed out; this one gets a fresh token pair so the user stays signed in here.
func (h *AuthHandler) ChangePassword(c *fiber.Ctx) error {
	var req dto.ChangePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var user models.User
	if err := h.db.First(&user, "id = ? AND org_id = ?", middleware.UserID(c), middleware.OrgID(c)).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "account not found")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: []string{"current password is incorrect"}})
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&user).Update("password_hash", hash).Error; err != nil {
			return err
		}
		return revokeSessions(tx, user.ID)
	}); err != nil {
		return err
	}
	return h.issuePair(c, &user)
}

// ForgotPassword always answers 204, whether or not the address has an account, so it
// cannot be used to discover who is registered. Inactive accounts get no email.
func (h *AuthHandler) ForgotPassword(c *fiber.Ctx) error {
	var req dto.ForgotPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if req.Email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: []string{"email is required"}})
	}

	var user models.User
	err := h.db.Where("email = ? AND is_active = ?", req.Email, true).First(&user).Error
	if err == nil {
		if err := startPasswordReset(h.db, h.mail, user, false); err != nil {
			log.Printf("password reset for %s failed: %v", user.ID, err)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("password reset lookup failed: %v", err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ResetPassword consumes a reset token and sets the new password. Every session of the
// account is signed out.
func (h *AuthHandler) ResetPassword(c *fiber.Ctx) error {
	var req dto.ResetPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	errInvalid := errors.New("invalid reset token")
	err = h.db.Transaction(func(tx *gorm.DB) error {
		var token models.PasswordResetToken
		// The conditional UPDATE is the single-use guard: of two concurrent requests with
		// the same token, only one can flip used_at.
		now := time.Now()
		res := tx.Model(&token).
			Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", auth.HashToken(req.Token), now).
			Update("used_at", &now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errInvalid
		}
		if err := tx.Where("token_hash = ?", auth.HashToken(req.Token)).First(&token).Error; err != nil {
			return err
		}
		var user models.User
		if err := tx.Where("id = ? AND is_active = ?", token.UserID, true).First(&user).Error; err != nil {
			return errInvalid
		}
		if err := tx.Model(&user).Update("password_hash", hash).Error; err != nil {
			return err
		}
		return revokeSessions(tx, user.ID)
	})
	if errors.Is(err, errInvalid) {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: []string{"this reset link is invalid or has expired"}})
	}
	if err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}
