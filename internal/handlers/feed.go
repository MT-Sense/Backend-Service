package handlers

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

type FeedHandler struct {
	db         *gorm.DB
	voteSecret string
	orgID      string
}

func NewFeedHandler(db *gorm.DB, voteSecret string, orgID string) *FeedHandler {
	return &FeedHandler{db: db, voteSecret: voteSecret, orgID: orgID}
}

// List serves the public feed. The two-gate rule is a WHERE clause, not a display filter:
// a post that has not been both opted into and published is never selected, so no amount of
// client tampering can surface one.
func (h *FeedHandler) List(c *fiber.Ctx) error {
	query := h.db.Model(&models.FeedPost{}).Where("org_id = ? AND opted_in = ? AND published = ?", h.orgID, true, true)

	if tag := c.Query("tag"); tag != "" {
		// hashtags is a JSON array column; match membership rather than substring.
		query = query.Where("hashtags::jsonb @> ?", `["`+tag+`"]`)
	}

	switch c.Query("sort") {
	case "newest":
		query = query.Order("posted_on DESC, id")
	default:
		query = query.Order("(upvotes - downvotes) DESC, id")
	}

	limit := c.QueryInt("limit", 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := c.QueryInt("offset", 0)
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return err
	}

	var posts []models.FeedPost
	if err := query.Limit(limit).Offset(offset).Find(&posts).Error; err != nil {
		return err
	}

	return c.JSON(dto.FeedList{
		Posts:  dto.NewFeedPosts(posts),
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// Vote records one vote per person per post. The voter is stored as a keyed hash of
// (secret, user, post): enough to reject a second vote on that post, useless for
// reconstructing which opinions a given person endorsed.
func (h *FeedHandler) Vote(c *fiber.Ctx) error {
	postID := c.Params("id")

	var req dto.VoteRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var post models.FeedPost
	err := h.db.Where("id = ? AND org_id = ? AND opted_in = ? AND published = ?", postID, h.orgID, true, true).First(&post).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "post not found")
	}
	if err != nil {
		return err
	}

	voter := auth.VoterHash(h.voteSecret, middleware.UserID(c), postID)

	err = h.db.Transaction(func(tx *gorm.DB) error {
		vote := models.FeedVote{PostID: postID, VoterHash: voter, Direction: req.Direction, CreatedAt: time.Now()}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&vote)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errAlreadyVoted
		}
		column := "upvotes"
		if req.Direction == -1 {
			column = "downvotes"
		}
		return tx.Model(&models.FeedPost{}).Where("id = ?", postID).
			UpdateColumn(column, gorm.Expr(column+" + 1")).Error
	})
	if errors.Is(err, errAlreadyVoted) {
		return fiber.NewError(fiber.StatusConflict, "you have already voted on this post")
	}
	if err != nil {
		return err
	}

	if err := h.db.First(&post, "id = ?", postID).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewFeedPost(&post))
}

var errAlreadyVoted = errors.New("already voted")

// Summaries serves what HR has explicitly published — never live aggregates, so employees
// only ever see reviewed content.
func (h *FeedHandler) Summaries(c *fiber.Ctx) error {
	var summaries []models.PublishedSummary
	if err := h.db.Where("org_id = ?", h.orgID).Order("published_at DESC").Find(&summaries).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewPublishedSummaries(summaries))
}

// ActionItems lists what the company has committed to, for the feed sidebar.
func (h *FeedHandler) ActionItems(c *fiber.Ctx) error {
	var items []models.ActionItem
	if err := h.db.Where("org_id = ?", h.orgID).Order("created_at DESC").Find(&items).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewActionItems(items))
}

// CreateActionItem lets HR or Executive open a follow-up. Executive-authored items are
// marked decision-level; HR-authored (admin) items are full-level.
func (h *FeedHandler) CreateActionItem(c *fiber.Ctx) error {
	var req dto.CreateActionItemRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	role := middleware.CurrentRole(c)
	level := "full"
	if role == models.RoleExecutive {
		level = "decision"
	}

	item := models.ActionItem{
		ID:         uuid.NewString(),
		OrgID:      h.orgID,
		Topic:      req.Topic,
		TopicID:    req.TopicID,
		Assignee:   req.Assignee,
		Status:     "in_progress",
		CreatedBy:  role,
		Level:      level,
		TargetDate: req.TargetDate,
		CreatedAt:  time.Now(),
	}
	if err := h.db.Create(&item).Error; err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.NewActionItem(&item))
}

// Moderate is gate two of the publish flow: HR reviewing a post that its author already
// consented to share. It cannot publish a post whose author never opted in.
func (h *FeedHandler) Moderate(c *fiber.Ctx) error {
	var req dto.ModerateRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if req.IsEmpty() {
		return fiber.NewError(fiber.StatusBadRequest, "nothing to update")
	}

	var post models.FeedPost
	if err := h.db.Where("id = ? AND org_id = ?", c.Params("id"), h.orgID).First(&post).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post not found")
	}

	updates := map[string]any{}
	if req.Published != nil {
		if *req.Published && !post.OptedIn {
			return fiber.NewError(fiber.StatusForbidden, "cannot publish a post the author did not opt in to share")
		}
		updates["published"] = *req.Published
	}
	if req.HRReplied != nil {
		updates["hr_replied"] = *req.HRReplied
	}

	if err := h.db.Model(&post).Updates(updates).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewFeedPost(&post))
}

// PendingModeration lists opted-in posts still awaiting HR review (HR only).
func (h *FeedHandler) PendingModeration(c *fiber.Ctx) error {
	var posts []models.FeedPost
	err := h.db.Where("org_id = ? AND opted_in = ? AND published = ?", h.orgID, true, false).
		Order("posted_on DESC").Find(&posts).Error
	if err != nil {
		return err
	}
	return c.JSON(dto.NewFeedPosts(posts))
}
