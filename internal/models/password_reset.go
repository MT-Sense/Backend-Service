package models

import "time"

// PasswordResetToken backs the "reset your password" email link. Only the SHA-256 of the
// token is stored (like RefreshToken), so a database leak does not yield usable links.
// A token is single-use (UsedAt) and short-lived (ExpiresAt).
type PasswordResetToken struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    string    `gorm:"size:64;not null;index"`
	TokenHash string    `gorm:"size:128;not null;uniqueIndex"`
	ExpiresAt time.Time `gorm:"not null"`
	UsedAt    *time.Time
	CreatedAt time.Time
}

func (PasswordResetToken) TableName() string { return "password_reset_tokens" }
