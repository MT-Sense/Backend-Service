package router

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/handlers"
	"github.com/mt-sense/backend-service/internal/knowledgebase"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// onboardingLimiter is a per-route, per-IP limiter for the public onboarding endpoints — the
// guessing surface for the 6-char join code and the optional company password. Deliberately
// not a global app.Use(...) so normal authenticated traffic is never throttled by it.
func onboardingLimiter(max int, expiration time.Duration) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: expiration,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
	})
}

// Register wires every route.
//
// Role guards are attached per route rather than via Group("", mw): a group with an empty
// prefix applies its middleware to every route registered after it on the parent, which
// silently makes later groups inherit earlier role checks. Naming the guard on each route
// keeps the permission matrix readable and impossible to inherit by accident.
//
// Org resolution is per-request: after login, org comes from the JWT claim (middleware.OrgID);
// before login (join-by-code), it comes from the join code itself. No handler holds a
// boot-time org constant anymore — analytics.Service is shared read-only across requests and
// cloned per-request via Service.WithOrg.
func Register(app *fiber.App, db *gorm.DB, cfg *config.Config) {
	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL)
	stats := analytics.New(db)
	ai := aiservice.New(cfg.AIServiceURL, 25*time.Second)
	kb := knowledgebase.New(db, ai)

	authH := handlers.NewAuthHandler(db, issuer)
	dashH := handlers.NewDashboardHandler(db, stats, ai)
	surveyH := handlers.NewSurveyHandler(db, stats, ai)
	trainingH := handlers.NewModelTrainingHandler(db, ai, cfg.AITrainingToken)
	feedH := handlers.NewFeedHandler(db, cfg.JWTSecret)
	periodsH := handlers.NewPeriodsHandler(db, stats, ai, kb)
	knowledgeH := handlers.NewKnowledgeBaseHandler(kb)
	onboardH := handlers.NewOnboardingHandler(db, issuer)
	departmentsH := handlers.NewDepartmentsHandler(db)
	automationH := handlers.NewAutomationHandler(db, cfg.AutomationEncryptionKey, cfg.AIServiceURL, cfg.AutomationServiceToken)

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

	// Self-service onboarding — see internal/handlers/onboarding.go. Rate limited (§4 of the
	// onboarding plan): these are the guessing surface for the 6-char join code and the
	// optional company password.
	api.Post("/onboarding/signup", onboardingLimiter(5, 10*time.Minute), onboardH.Signup)
	api.Post("/onboarding/join/check", onboardingLimiter(10, time.Minute), onboardH.CheckJoinCode)
	api.Post("/onboarding/join/verify-password", onboardingLimiter(10, time.Minute), onboardH.CheckCompanyPassword)
	api.Post("/onboarding/join/register", onboardingLimiter(5, time.Minute), onboardH.RegisterEmployee)

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
	r.Get("/survey-questions/catalog", surveyH.Catalog)

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
	r.Get("/dashboard/hr/trend", adminOnly, dashH.DepartmentTrend)
	r.Get("/dashboard/hr/heatmap", adminOnly, dashH.Heatmap)
	r.Get("/dashboard/hr/departments", adminOnly, dashH.DepartmentSummary)
	r.Get("/dashboard/hr/wordcloud", adminOnly, dashH.WordCloud)
	r.Get("/dashboard/hr/insight", adminOnly, dashH.Insight)
	r.Get("/dashboard/hr/alerts", adminOnly, dashH.Alerts)
	r.Get("/dashboard/hr/extra-questions", adminOnly, dashH.ExtraQuestions)
	r.Get("/dashboard/hr/topics/:id", adminOnly, dashH.TopicDrilldown)
	r.Get("/hr/departments", adminOnly, departmentsH.List)
	r.Post("/hr/departments", adminOnly, departmentsH.Create)
	r.Patch("/hr/departments/:id", adminOnly, departmentsH.Update)
	r.Delete("/hr/departments/:id", adminOnly, departmentsH.Delete)
	r.Post("/ai/train", adminOnly, trainingH.Train)
	r.Get("/survey-periods", adminOnly, periodsH.List)
	r.Post("/survey-periods", adminOnly, periodsH.Create)
	r.Post("/survey-periods/:id/close", adminOnly, periodsH.Close)
	r.Post("/survey-periods/:id/import/preview", adminOnly, periodsH.PreviewImport)
	r.Post("/survey-periods/:id/import", adminOnly, periodsH.ImportWorkbook)
	r.Get("/feed/pending", adminOnly, feedH.PendingModeration)
	r.Patch("/feed/:id/moderate", adminOnly, feedH.Moderate)
	r.Get("/org/join-code", adminOnly, onboardH.GetJoinCode)
	r.Post("/org/join-code/regenerate", adminOnly, onboardH.RegenerateJoinCode)
	r.Get("/org/settings", adminOnly, onboardH.GetOrgSettings)
	r.Patch("/org/settings", adminOnly, onboardH.UpdateOrgSettings)
	r.Post("/knowledge-base/:periodId/compile", adminOnly, knowledgeH.Compile)

	// Compiled knowledge contains aggregates only, so HR and executives can browse it.
	r.Get("/knowledge-base", leadership, knowledgeH.List)
	r.Get("/knowledge-base/index", leadership, knowledgeH.Index)
	r.Post("/knowledge-base/ask", leadership, knowledgeH.Ask)
	r.Get("/knowledge-base/:periodId", leadership, knowledgeH.Get)

	r.Get("/hr/automation/routing-policy", adminOnly, automationH.RoutingPolicy)
	r.Patch("/hr/automation/routing-policy", adminOnly, automationH.SaveRoutingPolicy)
	r.Get("/hr/automation/cases", adminOnly, automationH.ListRoutingCases)
	r.Post("/hr/automation/cases", adminOnly, onboardingLimiter(20, time.Minute), automationH.CreateRoutingCase)
	r.Post("/hr/automation/cases/:id", adminOnly, automationH.UpdateRoutingCase)
	r.Get("/hr/automation/settings", adminOnly, automationH.GetSettings)
	r.Patch("/hr/automation/settings", adminOnly, automationH.SaveSettings)
	r.Post("/hr/automation/jira/test", adminOnly, automationH.TestJira)
	r.Get("/hr/automation/proposals", adminOnly, automationH.ListProposals)
	r.Post("/hr/automation/generate", adminOnly, onboardingLimiter(5, time.Minute), automationH.Generate)
	r.Post("/hr/automation/demo", adminOnly, onboardingLimiter(5, time.Minute), automationH.Demo)
	r.Post("/hr/automation/proposals/:id/review", adminOnly, automationH.Review)
	r.Post("/hr/automation/proposals/:id/execute", adminOnly, automationH.Execute)
	r.Post("/hr/automation/proposals/:id/complete", adminOnly, automationH.Complete)
	r.Get("/hr/automation/proposals/:id/events", adminOnly, automationH.Events)

	// Executive only — aggregates, no raw text by construction.
	r.Get("/dashboard/executive/summary", execOnly, dashH.ExecutiveSummary)

	// Admin (HR) or Executive — both can open follow-ups; the handler marks Executive ones
	// decision-level.
	r.Post("/action-items", leadership, feedH.CreateActionItem)
}
