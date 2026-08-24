// Package middleware wires authentication and authorization as two separate layers:
// RequireAuth proves who you are, RequireRole decides what that lets you reach. Keeping
// them apart means a handler can never be reachable by forgetting one of the two.
package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/models"
)

const (
	ctxUserID = "userID"
	ctxOrgID  = "orgID"
	ctxRole   = "role"
)

// RequireAuth rejects any request without a valid access token.
func RequireAuth(issuer *auth.Issuer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get("Authorization")
		if header == "" {
			return fiber.NewError(fiber.StatusUnauthorized, "missing authorization header")
		}
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return fiber.NewError(fiber.StatusUnauthorized, "malformed authorization header")
		}

		claims, err := issuer.Parse(strings.TrimSpace(parts[1]))
		if err != nil {
			return fiber.NewError(fiber.StatusUnauthorized, "invalid or expired token")
		}

		c.Locals(ctxUserID, claims.UserID)
		c.Locals(ctxOrgID, claims.OrgID)
		c.Locals(ctxRole, claims.Role)
		return c.Next()
	}
}

// RequireRole gates a route to specific roles. This is the enforcement point the spec
// requires to live server-side: the Executive restriction on raw text and drill-downs is
// real here, not merely hidden in the UI.
func RequireRole(allowed ...models.Role) fiber.Handler {
	return func(c *fiber.Ctx) error {
		role, ok := c.Locals(ctxRole).(models.Role)
		if !ok {
			return fiber.NewError(fiber.StatusUnauthorized, "not authenticated")
		}
		for _, a := range allowed {
			if role == a {
				return c.Next()
			}
		}
		return fiber.NewError(fiber.StatusForbidden, "your role cannot access this resource")
	}
}

// UserID returns the authenticated user's id, or "" when unauthenticated.
func UserID(c *fiber.Ctx) string {
	id, _ := c.Locals(ctxUserID).(string)
	return id
}

// CurrentRole returns the authenticated user's role, or "" when unauthenticated.
func CurrentRole(c *fiber.Ctx) models.Role {
	role, _ := c.Locals(ctxRole).(models.Role)
	return role
}

// OrgID returns the authenticated user's organization id, or "" when unauthenticated. This
// is the per-request replacement for the old boot-time constant every handler used to share.
func OrgID(c *fiber.Ctx) string {
	id, _ := c.Locals(ctxOrgID).(string)
	return id
}
