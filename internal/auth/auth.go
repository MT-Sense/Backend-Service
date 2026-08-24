// Package auth issues and verifies the JWT access/refresh pair. Role lives in the access
// token claim, put there from the database record at login — a client can never assert it.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mt-sense/backend-service/internal/models"
)

var (
	ErrInvalidToken = errors.New("invalid or expired token")
	ErrWrongType    = errors.New("token used for the wrong purpose")
)

type Claims struct {
	UserID string      `json:"sub"`
	OrgID  string      `json:"org"`
	Role   models.Role `json:"role"`
	Type   string      `json:"typ"` // "access"
	jwt.RegisteredClaims
}

type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewIssuer(secret string, accessTTL, refreshTTL time.Duration) *Issuer {
	return &Issuer{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL}
}

func (i *Issuer) RefreshTTL() time.Duration { return i.refreshTTL }

// AccessToken signs a short-lived token carrying the user's id, org and role.
func (i *Issuer) AccessToken(userID, orgID string, role models.Role) (string, time.Time, error) {
	expiry := time.Now().Add(i.accessTTL)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: userID,
		OrgID:  orgID,
		Role:   role,
		Type:   "access",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ExpiresAt: jwt.NewNumericDate(expiry),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "mt-sense",
		},
	})
	signed, err := token.SignedString(i.secret)
	return signed, expiry, err
}

// RefreshToken returns an opaque random token plus the hash to store. The plaintext is
// handed to the client and never written down, so a dump of the table cannot be replayed.
func (i *Issuer) RefreshToken() (plaintext, hash string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", time.Time{}, fmt.Errorf("generating refresh token: %w", err)
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, HashToken(plaintext), time.Now().Add(i.refreshTTL), nil
}

func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Parse verifies signature, expiry and algorithm, and rejects anything that is not an
// access token.
func (i *Issuer) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return i.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("mt-sense"))

	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	if claims.Type != "access" {
		return nil, ErrWrongType
	}
	return claims, nil
}

// AnonymousToken is the per-submission identifier that replaces the user id on a survey
// response. It is generated fresh from crypto/rand and is deliberately not derived from
// anything about the user — nothing can invert it back to a person.
func AnonymousToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating anonymous token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// VoterHash lets the feed dedupe votes without storing who voted on what. It is a keyed
// digest of (user, post): usable to spot a repeat vote on that one post, useless for
// assembling a person's voting history without the server secret.
func VoterHash(secret, userID, postID string) string {
	sum := sha256.Sum256([]byte(secret + "|" + userID + "|" + postID))
	return hex.EncodeToString(sum[:])[:64]
}

// joinCodeCharset excludes visually ambiguous characters (0/O, 1/I/L) since a join code is
// read aloud or copied off a printout by hand, not pasted from a password manager.
const joinCodeCharset = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// JoinCode generates a 6-character, case-insensitive-by-convention (always produced
// uppercase) join code. Collision handling is the caller's responsibility via a DB unique
// constraint + retry loop — this function has no knowledge of existing codes.
func JoinCode() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating join code: %w", err)
	}
	out := make([]byte, 6)
	for i, b := range raw {
		out[i] = joinCodeCharset[int(b)%len(joinCodeCharset)]
	}
	return string(out), nil
}
