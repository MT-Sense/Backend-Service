package handlers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type topicSelection struct {
	OrgID          string
	Period         *models.SurveyPeriod
	TopicID        string
	Label          models.Localized
	DepartmentID   string
	DepartmentName string
}

func (h *DashboardHandler) resolveTopicSelection(c *fiber.Ctx) (topicSelection, error) {
	scope := topicSelection{OrgID: middleware.OrgID(c), TopicID: c.Params("id"), DepartmentID: c.Query("department")}
	period, err := h.resolvePeriod(c, h.stats.WithOrg(scope.OrgID))
	if err != nil {
		return scope, fiber.NewError(404, "no survey period found")
	}
	scope.Period = period
	var topic models.Topic
	if err := h.db.First(&topic, "id = ?", scope.TopicID).Error; err == nil {
		scope.Label = topic.Label
	} else {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return scope, err
		}
		var emerging models.EmergingTopic
		if err := h.db.Where("org_id = ? AND id = ?", scope.OrgID, scope.TopicID).First(&emerging).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return scope, err
			}
			return scope, fiber.NewError(404, "unknown topic")
		}
		scope.Label = emerging.Label
	}
	if scope.DepartmentID != "" {
		scope.DepartmentName = "Unassigned"
		if scope.DepartmentID != analytics.UnassignedDepartmentID {
			var department models.Department
			if err := h.db.Where("org_id = ? AND id = ?", scope.OrgID, scope.DepartmentID).First(&department).Error; err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return scope, err
				}
				return scope, fiber.NewError(404, "unknown department")
			}
			scope.DepartmentName = department.Name
		}
	}
	return scope, nil
}

type topicSummaryTheme struct {
	Title           models.Localized `json:"title"`
	Summary         models.Localized `json:"summary"`
	SupportingCount int              `json:"supportingCount"`
	Excerpts        []string         `json:"excerpts"`
}

func validateTopicThemes(input []aiservice.TopicComment, result *aiservice.TopicSummaryResult) ([]topicSummaryTheme, error) {
	invalid := errors.New("invalid topic summary evidence")
	if result == nil || result.Themes == nil || len(result.Themes) > 3 {
		return nil, invalid
	}
	sources := make(map[string]string, len(input))
	for _, comment := range input {
		sources[comment.Ref] = comment.Text
	}
	out := make([]topicSummaryTheme, 0, len(result.Themes))
	validText := func(text string, maximum int) bool {
		n := utf8.RuneCountInString(strings.TrimSpace(text))
		return n >= 2 && n <= maximum
	}
	for _, theme := range result.Themes {
		if !validText(theme.TitleTH, 120) || !validText(theme.TitleEN, 120) || !validText(theme.SummaryTH, 500) || !validText(theme.SummaryEN, 500) || len(theme.Evidence) < privacy.MinGroupSizeSQL || len(theme.Evidence) > len(input) {
			return nil, invalid
		}
		seen := map[string]bool{}
		excerpts := []string{}
		for _, evidence := range theme.Evidence {
			text, exists := sources[evidence.Ref]
			n := utf8.RuneCountInString(evidence.Excerpt)
			if !exists || seen[evidence.Ref] || n < 12 || n > 240 || !strings.Contains(text, evidence.Excerpt) {
				return nil, invalid
			}
			seen[evidence.Ref] = true
			excerpt := privacy.Redact(evidence.Excerpt)
			duplicate := false
			for _, prior := range excerpts {
				if prior == excerpt {
					duplicate = true
				}
			}
			if !duplicate && len(excerpts) < 2 {
				excerpts = append(excerpts, excerpt)
			}
		}
		out = append(out, topicSummaryTheme{
			Title:           models.Localized{TH: privacy.Redact(strings.TrimSpace(theme.TitleTH)), EN: privacy.Redact(strings.TrimSpace(theme.TitleEN))},
			Summary:         models.Localized{TH: privacy.Redact(strings.TrimSpace(theme.SummaryTH)), EN: privacy.Redact(strings.TrimSpace(theme.SummaryEN))},
			SupportingCount: len(seen), Excerpts: excerpts,
		})
	}
	return out, nil
}

// GenerateTopicSummary is POST-only and HR-only. No GET handler or background job calls it.
func (h *DashboardHandler) GenerateTopicSummary(c *fiber.Ctx) error {
	scope, err := h.resolveTopicSelection(c)
	if err != nil {
		return err
	}
	if h.summaryToken == "" {
		return c.Status(503).JSON(fiber.Map{"code": "summary_unavailable"})
	}
	comments, err := h.stats.WithOrg(scope.OrgID).TopicSummaryEvidence(scope.Period.ID, scope.TopicID, scope.DepartmentID)
	if err != nil {
		return err
	}
	if len(comments) < privacy.MinGroupSizeSQL {
		return c.Status(422).JSON(fiber.Map{"code": "insufficient_summary_evidence"})
	}
	label := strings.TrimSpace(privacy.Redact(scope.Label.TH))
	if label == "" {
		label = strings.TrimSpace(privacy.Redact(scope.Label.EN))
	}
	if utf8.RuneCountInString(label) > 150 {
		label = string([]rune(label)[:150])
	}
	result, err := h.ai.SummarizeTopic(c.UserContext(), aiservice.TopicSummaryRequest{Topic: label, Comments: comments}, h.summaryToken)
	if err != nil {
		var serviceError *aiservice.TopicSummaryError
		if errors.As(err, &serviceError) {
			return c.Status(502).JSON(fiber.Map{"code": serviceError.Code})
		}
		return c.Status(502).JSON(fiber.Map{"code": "summary_unavailable"})
	}
	themes, err := validateTopicThemes(comments, result)
	if err != nil {
		return c.Status(502).JSON(fiber.Map{"code": "summary_invalid_evidence"})
	}
	return c.JSON(fiber.Map{
		"topicId": scope.TopicID, "periodId": scope.Period.ID, "departmentId": scope.DepartmentID,
		"sampledComments": len(comments), "themes": themes,
		"generatedAt": time.Now().UTC(), "model": "gemini-3.5-flash-lite",
	})
}
