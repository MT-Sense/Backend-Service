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
//
// orgID is the single seeded organization's id — the app is single-tenant for now (see
// README), but every query is already scoped by it so multi-tenancy is a matter of deriving
// orgID per-request later rather than retrofitting every handler.
func Register(app *fiber.App, db *gorm.DB, cfg *config.Config, orgID string) {
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL)
	stats := analytics.New(db, orgID)

	authH := handlers.NewAuthHandler(db, issuer, orgID)
	dashH := handlers.NewDashboardHandler(db, stats, orgID)
	surveyH := handlers.NewSurveyHandler(db, stats, orgID)
	feedH := handlers.NewFeedHandler(db, cfg.JWTSecret, orgID)
	periodsH := handlers.NewPeriodsHandler(db, stats, orgID)

	adminOnly := middleware.RequireRole(models.RoleAdmin)
	execOnly := middleware.RequireRole(models.RoleExecutive)
	leadership := middleware.RequireRole(models.RoleAdmin, models.RoleExecutive)

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
	r.Get("/positions", dashH.Positions)

	// Employee-facing surfaces — open to all three roles, since admin (HR) and executives
	// also fill in surveys and read the feed.
	r.Get("/surveys/current", surveyH.Current)
	r.Post("/surveys/current/responses", surveyH.Submit)
	r.Get("/feed", feedH.List)
	r.Post("/feed/:id/vote", feedH.Vote)
	r.Get("/summaries", feedH.Summaries)
	r.Get("/action-items", feedH.ActionItems)

	// Admin (HR) only — analysis, drill-downs (the only endpoint returning sample text),
	// survey-period scheduling, and feed moderation.
	r.Get("/dashboard/hr/kpi", adminOnly, dashH.HRKpis)
	r.Get("/dashboard/hr/heatmap", adminOnly, dashH.Heatmap)
	r.Get("/dashboard/hr/wordcloud", adminOnly, dashH.WordCloud)
	r.Get("/dashboard/hr/insight", adminOnly, dashH.Insight)
	r.Get("/dashboard/hr/alerts", adminOnly, dashH.Alerts)
	r.Get("/dashboard/hr/topics/:id", adminOnly, dashH.TopicDrilldown)
	r.Get("/survey-periods", adminOnly, periodsH.List)
	r.Post("/survey-periods", adminOnly, periodsH.Create)
	r.Post("/survey-periods/:id/close", adminOnly, periodsH.Close)
	r.Get("/feed/pending", adminOnly, feedH.PendingModeration)
	r.Patch("/feed/:id/moderate", adminOnly, feedH.Moderate)

	// Executive only — aggregates, no raw text by construction.
	r.Get("/dashboard/executive/summary", execOnly, dashH.ExecutiveSummary)

	// Admin (HR) or Executive — both can open follow-ups; the handler marks Executive ones
	// decision-level.
	r.Post("/action-items", leadership, feedH.CreateActionItem)
}
