package handlers

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/mailer"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// UsersHandler is HR's account management: profile, department, position, role and access.
// It never reads survey tables — who answered (or what) is not part of account management.
type UsersHandler struct {
	db   *gorm.DB
	mail *mailer.Service
}

func NewUsersHandler(db *gorm.DB, mail *mailer.Service) *UsersHandler {
	return &UsersHandler{db: db, mail: mail}
}

var (
	errLastAdmin = fiber.NewError(fiber.StatusConflict, "the organization must keep at least one active HR account")
	errSelfEdit  = fiber.NewError(fiber.StatusForbidden, "you cannot change your own role or deactivate yourself")
	errEmailUsed = fiber.NewError(fiber.StatusConflict, "this email is already used by another account")
)

func (h *UsersHandler) List(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	var users []models.User
	if err := h.db.Where("org_id = ?", orgID).Order("full_name, email").Find(&users).Error; err != nil {
		return err
	}
	departments, positions, err := h.lookups(orgID)
	if err != nil {
		return err
	}
	out := make([]dto.ManagedUser, 0, len(users))
	for i := range users {
		out = append(out, dto.NewManagedUser(&users[i], departments, positions))
	}
	return c.JSON(out)
}

func (h *UsersHandler) Create(c *fiber.Ctx) error {
	var req dto.CreateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}
	orgID := middleware.OrgID(c)
	hash, err := hashPassword(req.Password)
	if err != nil {
		return err
	}

	user := models.User{
		ID: uuid.NewString(), OrgID: orgID, Email: req.Email, PasswordHash: hash,
		Role: req.Role, FullName: req.FullName, IsActive: true,
		NotifyNewRound: true, NotifyMonthlySummary: true,
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := emailFree(tx, req.Email, ""); err != nil {
			return err
		}
		departmentID, err := orgDepartment(tx, orgID, req.DepartmentID)
		if err != nil {
			return err
		}
		positionID, err := orgPosition(tx, orgID, req.Position)
		if err != nil {
			return err
		}
		user.DepartmentID, user.PositionID = departmentID, positionID
		return tx.Create(&user).Error
	})
	if err != nil {
		return err
	}
	return h.respond(c, &user, fiber.StatusCreated)
}

func (h *UsersHandler) Update(c *fiber.Ctx) error {
	var req dto.UpdateUserRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if req.IsEmpty() {
		return fiber.NewError(fiber.StatusBadRequest, "nothing to update")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}
	orgID, callerID := middleware.OrgID(c), middleware.UserID(c)

	var user models.User
	err := h.db.Transaction(func(tx *gorm.DB) error {
		// Lock the org's HR rows so two concurrent demotions cannot both pass the
		// last-admin check.
		var admins []models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("org_id = ? AND role = ? AND is_active = ?", orgID, models.RoleAdmin, true).
			Find(&admins).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id = ? AND id = ?", orgID, c.Params("id")).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fiber.NewError(fiber.StatusNotFound, "user not found")
			}
			return err
		}

		roleChanges := req.Role != nil && *req.Role != user.Role
		deactivates := req.IsActive != nil && !*req.IsActive && user.IsActive
		if user.ID == callerID && (roleChanges || deactivates) {
			return errSelfEdit
		}
		losesAdmin := user.Role == models.RoleAdmin && user.IsActive && (deactivates || (roleChanges && *req.Role != models.RoleAdmin))
		if losesAdmin && len(admins) <= 1 {
			return errLastAdmin
		}

		updates := map[string]any{}
		if req.FullName != nil {
			updates["full_name"] = *req.FullName
		}
		emailChanges := req.Email != nil && *req.Email != user.Email
		if emailChanges {
			if err := emailFree(tx, *req.Email, user.ID); err != nil {
				return err
			}
			updates["email"] = *req.Email
		}
		if roleChanges {
			updates["role"] = *req.Role
		}
		if req.IsActive != nil {
			updates["is_active"] = *req.IsActive
		}
		if req.DepartmentID != nil {
			departmentID, err := orgDepartment(tx, orgID, *req.DepartmentID)
			if err != nil {
				return err
			}
			updates["department_id"] = departmentID
		}
		if req.Position != nil {
			positionID, err := orgPosition(tx, orgID, *req.Position)
			if err != nil {
				return err
			}
			updates["position_id"] = positionID
		}
		if err := tx.Model(&user).Updates(updates).Error; err != nil {
			return err
		}
		// Access is derived from the token's claims; ending sessions makes a new role,
		// email or deactivation apply at the next refresh instead of at token expiry.
		if roleChanges || deactivates || emailChanges {
			if err := revokeSessions(tx, user.ID); err != nil {
				return err
			}
		}
		return tx.First(&user, "id = ?", user.ID).Error
	})
	if err != nil {
		return err
	}
	return h.respond(c, &user, fiber.StatusOK)
}

// SetPassword gives the account a temporary password HR passes on; all sessions end.
func (h *UsersHandler) SetPassword(c *fiber.Ctx) error {
	var req dto.SetPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}
	user, err := h.find(c)
	if err != nil {
		return err
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		return err
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(user).Update("password_hash", hash).Error; err != nil {
			return err
		}
		return revokeSessions(tx, user.ID)
	}); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// SendResetEmail emails the person a link to choose their own password.
func (h *UsersHandler) SendResetEmail(c *fiber.Ctx) error {
	user, err := h.find(c)
	if err != nil {
		return err
	}
	if !user.IsActive {
		return fiber.NewError(fiber.StatusConflict, "activate the account before sending a reset link")
	}
	if err := startPasswordReset(h.db, h.mail, *user, true); err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"sentTo": user.Email, "smtpConfigured": h.mail.Configured()})
}

func (h *UsersHandler) find(c *fiber.Ctx) (*models.User, error) {
	var user models.User
	err := h.db.Where("org_id = ? AND id = ?", middleware.OrgID(c), c.Params("id")).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fiber.NewError(fiber.StatusNotFound, "user not found")
	}
	return &user, err
}

func (h *UsersHandler) respond(c *fiber.Ctx, user *models.User, status int) error {
	departments, positions, err := h.lookups(user.OrgID)
	if err != nil {
		return err
	}
	return c.Status(status).JSON(dto.NewManagedUser(user, departments, positions))
}

func (h *UsersHandler) lookups(orgID string) (departments, positions map[string]string, err error) {
	var deps []models.Department
	var pos []models.Position
	if err = h.db.Select("id", "name").Where("org_id = ?", orgID).Find(&deps).Error; err != nil {
		return nil, nil, err
	}
	if err = h.db.Select("id", "name").Where("org_id = ?", orgID).Find(&pos).Error; err != nil {
		return nil, nil, err
	}
	departments = make(map[string]string, len(deps))
	for _, d := range deps {
		departments[d.ID] = d.Name
	}
	positions = make(map[string]string, len(pos))
	for _, p := range pos {
		positions[p.ID] = p.Name
	}
	return departments, positions, nil
}

// emailFree checks the globally unique email is not taken by anyone but exceptID.
func emailFree(tx *gorm.DB, email, exceptID string) error {
	var n int64
	q := tx.Model(&models.User{}).Where("email = ?", email)
	if exceptID != "" {
		q = q.Where("id <> ?", exceptID)
	}
	if err := q.Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return errEmailUsed
	}
	return nil
}

// orgDepartment validates a department id belongs to the org; "" means none.
func orgDepartment(tx *gorm.DB, orgID, departmentID string) (*string, error) {
	if departmentID == "" {
		return nil, nil
	}
	var n int64
	if err := tx.Model(&models.Department{}).Where("org_id = ? AND id = ?", orgID, departmentID).Count(&n).Error; err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fiber.NewError(fiber.StatusBadRequest, "department not found")
	}
	return &departmentID, nil
}

// orgPosition resolves a position name within the org, creating it if new (positions are
// free text, as in self-service registration); "" means none.
func orgPosition(tx *gorm.DB, orgID, name string) (*string, error) {
	if name == "" {
		return nil, nil
	}
	var position models.Position
	err := tx.Where("org_id = ? AND name = ?", orgID, name).First(&position).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		position = models.Position{ID: uuid.NewString(), OrgID: orgID, Name: name, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		err = tx.Create(&position).Error
	}
	if err != nil {
		return nil, err
	}
	return &position.ID, nil
}
