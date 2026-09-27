package handlers

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/knowledgebase"
	"github.com/mt-sense/backend-service/internal/middleware"
)

type KnowledgeBaseHandler struct {
	service *knowledgebase.Service
}

func NewKnowledgeBaseHandler(service *knowledgebase.Service) *KnowledgeBaseHandler {
	return &KnowledgeBaseHandler{service: service}
}

type knowledgeArticleResponse struct {
	ID                 string   `json:"id"`
	PeriodID           string   `json:"periodId"`
	PeriodLabel        string   `json:"periodLabel"`
	TitleTH            string   `json:"titleTh"`
	TitleEN            string   `json:"titleEn"`
	SummaryTH          string   `json:"summaryTh"`
	SummaryEN          string   `json:"summaryEn"`
	Markdown           string   `json:"markdown,omitempty"`
	Tags               []string `json:"tags"`
	RelatedPeriodIDs   []string `json:"relatedPeriodIds"`
	SuggestedQuestions []string `json:"suggestedQuestions"`
	SourceHash         string   `json:"sourceHash"`
	CompiledAt         string   `json:"compiledAt"`
	Reused             *bool    `json:"reused,omitempty"`
}

func newKnowledgeArticleResponse(article *knowledgebase.Article, includeMarkdown bool) knowledgeArticleResponse {
	entry := article.Entry
	response := knowledgeArticleResponse{
		ID:                 entry.ID,
		PeriodID:           entry.PeriodID,
		PeriodLabel:        article.PeriodLabel,
		TitleTH:            entry.TitleTH,
		TitleEN:            entry.TitleEN,
		SummaryTH:          entry.SummaryText,
		SummaryEN:          entry.SummaryEN,
		Tags:               entry.Tags,
		RelatedPeriodIDs:   entry.RelatedPeriodIDs,
		SuggestedQuestions: entry.SuggestedQuestions,
		SourceHash:         entry.SourceHash,
		CompiledAt:         entry.CompiledAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if includeMarkdown {
		response.Markdown = entry.MarkdownText
	}
	return response
}

func (h *KnowledgeBaseHandler) List(c *fiber.Ctx) error {
	articles, err := h.service.List(middleware.OrgID(c))
	if err != nil {
		return err
	}
	out := make([]knowledgeArticleResponse, 0, len(articles))
	for i := range articles {
		out = append(out, newKnowledgeArticleResponse(&articles[i], false))
	}
	return c.JSON(out)
}

func (h *KnowledgeBaseHandler) Get(c *fiber.Ctx) error {
	article, err := h.service.Get(middleware.OrgID(c), c.Params("periodId"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "knowledge article not found")
	}
	if err != nil {
		return err
	}
	return c.JSON(newKnowledgeArticleResponse(article, true))
}

func (h *KnowledgeBaseHandler) Index(c *fiber.Ctx) error {
	markdown, err := h.service.IndexMarkdown(middleware.OrgID(c))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"markdown": markdown})
}

type knowledgeQuestionRequest struct {
	Question string `json:"question"`
	Locale   string `json:"locale"`
}

func (h *KnowledgeBaseHandler) Ask(c *fiber.Ctx) error {
	var req knowledgeQuestionRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		return fiber.NewError(fiber.StatusBadRequest, "question is required")
	}
	if len([]rune(req.Question)) > 1200 {
		return fiber.NewError(fiber.StatusBadRequest, "question is too long")
	}
	if req.Locale != "en" {
		req.Locale = "th"
	}
	result, err := h.service.Ask(c.UserContext(), middleware.OrgID(c), req.Question, req.Locale)
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, "knowledge Q&A failed: "+err.Error())
	}
	return c.JSON(result)
}

func (h *KnowledgeBaseHandler) Compile(c *fiber.Ctx) error {
	force, _ := strconv.ParseBool(c.Query("force", "false"))
	article, reused, err := h.service.CompilePeriod(
		c.UserContext(),
		middleware.OrgID(c),
		c.Params("periodId"),
		force,
	)
	if errors.Is(err, knowledgebase.ErrInsufficientData) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, err.Error())
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "survey period not found")
	}
	if err != nil {
		return fiber.NewError(fiber.StatusBadGateway, "knowledge compilation failed: "+err.Error())
	}
	response := newKnowledgeArticleResponse(article, true)
	response.Reused = &reused
	return c.JSON(response)
}
