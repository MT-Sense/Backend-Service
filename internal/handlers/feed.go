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
}

func NewFeedHandler(db *gorm.DB, voteSecret string) *FeedHandler {
	return &FeedHandler{db: db, voteSecret: voteSecret}
}

// List serves the public feed. The two-gate rule is a WHERE clause, not a display filter:
// a post that has not been both opted into and published is never selected, so no amount of
// client tampering can surface one.
func (h *FeedHandler) List(c *fiber.Ctx) error {
	query := h.db.Model(&models.FeedPost{}).Where("org_id = ? AND opted_in = ? AND published = ?", middleware.OrgID(c), true, true)

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

	out := dto.NewFeedPosts(posts)
	mine, err := h.myVotes(middleware.UserID(c), posts)
	if err != nil {
		return err
	}
	for i := range out {
		out[i].MyVote = mine[out[i].ID]
	}
	if err := h.attachActionItems(middleware.OrgID(c), posts, out); err != nil {
		return err
	}

	return c.JSON(dto.FeedList{
		Posts:  out,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// myVotes returns the caller's vote per post. Only the caller can derive their own voter
// hashes, so this reveals nothing about anyone else's votes.
func (h *FeedHandler) myVotes(userID string, posts []models.FeedPost) (map[string]int, error) {
	out := make(map[string]int, len(posts))
	if userID == "" || len(posts) == 0 {
		return out, nil
	}
	hashes := make([]string, 0, len(posts))
	ids := make([]string, 0, len(posts))
	for _, p := range posts {
		ids = append(ids, p.ID)
		hashes = append(hashes, auth.VoterHash(h.voteSecret, userID, p.ID))
	}
	var votes []models.FeedVote
	if err := h.db.Where("post_id IN ? AND voter_hash IN ?", ids, hashes).Find(&votes).Error; err != nil {
		return nil, err
	}
	for _, v := range votes {
		if v.VoterHash == auth.VoterHash(h.voteSecret, userID, v.PostID) {
			out[v.PostID] = v.Direction
		}
	}
	return out, nil
}

func voteColumn(direction int) string {
	if direction == -1 {
		return "downvotes"
	}
	return "upvotes"
}

// Vote sets the caller's vote on a post, Reddit-style: voting the same direction again
// removes the vote, voting the other direction switches it. One vote per person per post is
// kept by the (post, voter hash) unique index; the voter is a keyed hash of (secret, user,
// post), enough to find your own vote, useless for reconstructing anyone's opinions.
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
	err := h.db.Where("id = ? AND org_id = ? AND opted_in = ? AND published = ?", postID, middleware.OrgID(c), true, true).First(&post).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "post not found")
	}
	if err != nil {
		return err
	}

	voter := auth.VoterHash(h.voteSecret, middleware.UserID(c), postID)

	myVote := 0
	err = h.db.Transaction(func(tx *gorm.DB) error {
		// Lock the post so concurrent votes on it apply one at a time and the counters
		// always match the vote rows.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			First(&models.FeedPost{}, "id = ?", postID).Error; err != nil {
			return err
		}
		adjust := func(direction int, delta string) error {
			column := voteColumn(direction)
			return tx.Model(&models.FeedPost{}).Where("id = ?", postID).
				UpdateColumn(column, gorm.Expr("GREATEST("+column+" "+delta+" 1, 0)")).Error
		}

		var existing models.FeedVote
		err := tx.Where("post_id = ? AND voter_hash = ?", postID, voter).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			vote := models.FeedVote{PostID: postID, VoterHash: voter, Direction: req.Direction, CreatedAt: time.Now()}
			if err := tx.Create(&vote).Error; err != nil {
				return err
			}
			myVote = req.Direction
			return adjust(req.Direction, "+")
		case err != nil:
			return err
		case existing.Direction == req.Direction:
			if err := tx.Delete(&existing).Error; err != nil {
				return err
			}
			return adjust(req.Direction, "-")
		default:
			// Capture before updating: Update also rewrites existing.Direction in memory.
			previous := existing.Direction
			if err := tx.Model(&existing).Update("direction", req.Direction).Error; err != nil {
				return err
			}
			myVote = req.Direction
			if err := adjust(previous, "-"); err != nil {
				return err
			}
			return adjust(req.Direction, "+")
		}
	})
	if err != nil {
		return err
	}

	if err := h.db.First(&post, "id = ?", postID).Error; err != nil {
		return err
	}
	out := []dto.FeedPost{dto.NewFeedPost(&post)}
	out[0].MyVote = myVote
	if err := h.attachActionItems(middleware.OrgID(c), []models.FeedPost{post}, out); err != nil {
		return err
	}
	return c.JSON(out[0])
}

// attachActionItems fills out[i].ActionItem for posts linked to an action item of the org.
func (h *FeedHandler) attachActionItems(orgID string, posts []models.FeedPost, out []dto.FeedPost) error {
	ids := make([]string, 0, len(posts))
	for _, p := range posts {
		if p.ActionItemID != nil {
			ids = append(ids, *p.ActionItemID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var items []models.ActionItem
	if err := h.db.Where("org_id = ? AND id IN ?", orgID, ids).Find(&items).Error; err != nil {
		return err
	}
	byID := make(map[string]*models.ActionItem, len(items))
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	for i, p := range posts {
		if p.ActionItemID != nil {
			if item, ok := byID[*p.ActionItemID]; ok {
				out[i].ActionItem = dto.NewLinkedActionItem(item)
			}
		}
	}
	return nil
}

// Summaries serves what HR has explicitly published — never live aggregates or drafts, so
// employees only ever see reviewed content.
func (h *FeedHandler) Summaries(c *fiber.Ctx) error {
	var summaries []models.PublishedSummary
	if err := h.db.Where("org_id = ? AND status = ?", middleware.OrgID(c), models.SummaryPublished).
		Order("published_at DESC, created_at DESC").Find(&summaries).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewPublishedSummaries(summaries))
}

// ActionItems lists what the company has committed to, for the feed sidebar and the HR /
// executive management screens.
func (h *FeedHandler) ActionItems(c *fiber.Ctx) error {
	var items []models.ActionItem
	if err := h.db.Where("org_id = ?", middleware.OrgID(c)).Order("created_at DESC").Find(&items).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewActionItems(items, middleware.UserID(c)))
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
	if req.Assignee == "" {
		req.Assignee = "TBD"
	}
	level := "full"
	if role == models.RoleExecutive {
		level = "decision"
	}

	userID := middleware.UserID(c)
	item := models.ActionItem{
		ID:              uuid.NewString(),
		OrgID:           middleware.OrgID(c),
		Topic:           req.Topic,
		TopicID:         req.TopicID,
		Assignee:        req.Assignee,
		Status:          models.ActionItemInProgress,
		CreatedBy:       role,
		Level:           level,
		TargetDate:      req.TargetDate,
		CreatedAt:       time.Now(),
		CreatedByUserID: &userID,
	}
	if err := h.db.Create(&item).Error; err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.NewActionItemFor(&item, userID))
}

// editableActionItem loads an item the caller may change: HR can edit any item in the org,
// an executive only the ones they opened.
func (h *FeedHandler) editableActionItem(c *fiber.Ctx) (*models.ActionItem, error) {
	var item models.ActionItem
	if err := h.db.Where("id = ? AND org_id = ?", c.Params("id"), middleware.OrgID(c)).First(&item).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "action item not found")
	}
	if middleware.CurrentRole(c) != models.RoleAdmin &&
		(item.CreatedByUserID == nil || *item.CreatedByUserID != middleware.UserID(c)) {
		return nil, fiber.NewError(fiber.StatusForbidden, "you can only change action items you created")
	}
	return &item, nil
}

func (h *FeedHandler) UpdateActionItem(c *fiber.Ctx) error {
	var req dto.UpdateActionItemRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if req.IsEmpty() {
		return fiber.NewError(fiber.StatusBadRequest, "nothing to update")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}
	item, err := h.editableActionItem(c)
	if err != nil {
		return err
	}

	updates := map[string]any{}
	if req.Status != nil && *req.Status != item.Status {
		updates["status"] = *req.Status
		if *req.Status == models.ActionItemDone {
			updates["done_at"] = time.Now()
		} else {
			updates["done_at"] = nil
		}
	}
	if req.Assignee != nil {
		updates["assignee"] = *req.Assignee
	}
	if req.TargetDate != nil {
		updates["target_date"] = *req.TargetDate
	}
	if len(updates) > 0 {
		if err := h.db.Model(item).Updates(updates).Error; err != nil {
			return err
		}
	}
	if err := h.db.First(item, "id = ?", item.ID).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewActionItemFor(item, middleware.UserID(c)))
}

func (h *FeedHandler) DeleteActionItem(c *fiber.Ctx) error {
	item, err := h.editableActionItem(c)
	if err != nil {
		return err
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		// Posts that pointed at this item simply lose the link.
		if err := tx.Model(&models.FeedPost{}).Where("action_item_id = ?", item.ID).
			Update("action_item_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(item).Error
	}); err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
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
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var post models.FeedPost
	if err := h.db.Where("id = ? AND org_id = ?", c.Params("id"), middleware.OrgID(c)).First(&post).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "post not found")
	}

	updates := map[string]any{}
	if req.Published != nil {
		if *req.Published && !post.OptedIn {
			return fiber.NewError(fiber.StatusForbidden, "cannot publish a post the author did not opt in to share")
		}
		updates["published"] = *req.Published
		updates["moderated_at"] = time.Now()
	}
	switch {
	case req.HRReply != nil && *req.HRReply != "":
		updates["hr_reply"] = *req.HRReply
		updates["hr_replied"] = true
		updates["hr_replied_at"] = time.Now()
	case req.HRReply != nil:
		updates["hr_reply"] = ""
		updates["hr_replied"] = false
		updates["hr_replied_at"] = nil
	case req.HRReplied != nil:
		updates["hr_replied"] = *req.HRReplied
	}
	if req.ActionItemID != nil {
		if *req.ActionItemID == "" {
			updates["action_item_id"] = nil
		} else {
			var n int64
			if err := h.db.Model(&models.ActionItem{}).
				Where("org_id = ? AND id = ?", post.OrgID, *req.ActionItemID).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return fiber.NewError(fiber.StatusBadRequest, "action item not found")
			}
			updates["action_item_id"] = *req.ActionItemID
		}
	}

	if err := h.db.Model(&post).Updates(updates).Error; err != nil {
		return err
	}
	if err := h.db.First(&post, "id = ?", post.ID).Error; err != nil {
		return err
	}
	out := []dto.FeedPost{dto.NewFeedPost(&post)}
	if err := h.attachActionItems(post.OrgID, []models.FeedPost{post}, out); err != nil {
		return err
	}
	return c.JSON(out[0])
}

// PendingModeration lists opted-in posts HR has not decided on yet (HR only). Rejected posts
// have moderated_at set and drop out of this list.
func (h *FeedHandler) PendingModeration(c *fiber.Ctx) error {
	var posts []models.FeedPost
	err := h.db.Where("org_id = ? AND opted_in = ? AND published = ? AND moderated_at IS NULL", middleware.OrgID(c), true, false).
		Order("posted_on DESC").Find(&posts).Error
	if err != nil {
		return err
	}
	out := dto.NewFeedPosts(posts)
	if err := h.attachActionItems(middleware.OrgID(c), posts, out); err != nil {
		return err
	}
	return c.JSON(out)
}
