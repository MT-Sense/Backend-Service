package router

import (
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/handlers"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// Register wires every route.
//
// Role guards are attached per route rather than via Group("", mw): a group with an empty
// prefix applies its middleware to every route registered after it on the parent, which
// silently makes later groups inherit earlier role checks. Naming the guard on each route
// keeps the permission matrix readable and impossible to inherit by accident.
func Register(app *fiber.App, db *gorm.DB, cfg *config.Config) {
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL)
	stats := analytics.New(db)

	authH := handlers.NewAuthHandler(db, issuer)
	dashH := handlers.NewDashboardHandler(db, stats)
	surveyH := handlers.NewSurveyHandler(db)
	feedH := handlers.NewFeedHandler(db, cfg.JWTSecret)
	formsH := handlers.NewFormsHandler(db)

	hrOnly := middleware.RequireRole(models.RoleHR)
	execOnly := middleware.RequireRole(models.RoleExecutive)
	leadership := middleware.RequireRole(models.RoleHR, models.RoleExecutive)

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	api := app.Group("/api")

	// --- public ---
	api.Post("/auth/login", authH.Login)
	api.Post("/auth/refresh", authH.Refresh)

	// --- authenticated: everything below requires a valid access token ---
	r := api.Group("", middleware.RequireAuth(issuer))

	// Any role.
	r.Post("/auth/logout", authH.Logout)
	r.Get("/settings/me", authH.Me)
	r.Patch("/settings/me", authH.UpdateMe)
	r.Get("/settings/me/submissions", authH.SubmissionHistory)
	r.Get("/topics", dashH.Topics)
	r.Get("/departments", dashH.Departments)

	// Employee-facing surfaces — open to all three roles, since HR and executives also
	// fill in surveys and read the feed.
	r.Get("/surveys/:id", surveyH.Get)
	r.Post("/surveys/:id/responses", surveyH.Submit)
	r.Get("/feed", feedH.List)
	r.Post("/feed/:id/vote", feedH.Vote)
	r.Get("/summaries", feedH.Summaries)
	r.Get("/action-items", feedH.ActionItems)

	// HR only — analysis, drill-downs (the only endpoint returning sample text),
	// form authoring and feed moderation.
	r.Get("/dashboard/hr/kpi", hrOnly, dashH.HRKpis)
	r.Get("/dashboard/hr/heatmap", hrOnly, dashH.Heatmap)
	r.Get("/dashboard/hr/wordcloud", hrOnly, dashH.WordCloud)
	r.Get("/dashboard/hr/insight", hrOnly, dashH.Insight)
	r.Get("/dashboard/hr/topics/:id", hrOnly, dashH.TopicDrilldown)
	r.Get("/forms", hrOnly, formsH.List)
	r.Post("/forms", hrOnly, formsH.Create)
	r.Put("/forms/:id", hrOnly, formsH.Update)
	r.Post("/forms/:id/publish", hrOnly, formsH.Publish)
	r.Post("/forms/validate-question", hrOnly, formsH.ValidateQuestion)
	r.Get("/feed/pending", hrOnly, feedH.PendingModeration)
	r.Patch("/feed/:id/moderate", hrOnly, feedH.Moderate)

	// Executive only — aggregates, no raw text by construction.
	r.Get("/dashboard/executive/summary", execOnly, dashH.ExecutiveSummary)

	// HR or Executive — both can open follow-ups; the handler marks Executive ones
	// decision-level.
	r.Post("/action-items", leadership, formsH.CreateActionItem)
}
