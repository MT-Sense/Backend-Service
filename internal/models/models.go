package models

import (
	"encoding/json"
	"time"
)

// Localized mirrors the frontend's LocalizedText ({ th, en }). Stored as JSON in one column.
// Used only by additive, app-specific tables (Topic, FeedPost text, insights, …) that sit
// outside the user-supplied core schema — the core tables (Department, Position, …) use a
// single plain string column exactly as specified there, so those entity names are not
// translated; only UI chrome strings are (via i18n).
type Localized struct {
	TH string `json:"th"`
	EN string `json:"en"`
}

// Suppressible mirrors the frontend's Suppressible<T> discriminated union. It marshals to
// {"suppressed":true} or {"suppressed":false,"data":...} so the client type checks unchanged.
type Suppressible[T any] struct {
	Suppressed bool
	Data       T
}

func Visible[T any](data T) Suppressible[T] { return Suppressible[T]{Suppressed: false, Data: data} }
func Hidden[T any]() Suppressible[T]        { return Suppressible[T]{Suppressed: true} }

func (s Suppressible[T]) MarshalJSON() ([]byte, error) {
	if s.Suppressed {
		return json.Marshal(struct {
			Suppressed bool `json:"suppressed"`
		}{true})
	}
	return json.Marshal(struct {
		Suppressed bool `json:"suppressed"`
		Data       T    `json:"data"`
	}{false, s.Data})
}

// SentimentSplit mirrors the frontend's SentimentSplit — three percentages totalling 100.
type SentimentSplit struct {
	Positive int `json:"positive"`
	Neutral  int `json:"neutral"`
	Negative int `json:"negative"`
}

// MinGroupSize is the n<5 privacy threshold. Any aggregate over a group smaller than this
// must be suppressed before it leaves the server — see privacy.SuppressBelowThreshold.
const MinGroupSize = 5

// ============================================================
// Core schema — mirrors the organizations/departments/positions/users/survey_* SQL supplied
// by the user field-for-field. Table/column names are set explicitly via gorm tags so the
// generated DDL matches that schema rather than GORM's own naming conventions.
// ============================================================

type Role string

const (
	RoleAdmin     Role = "admin" // renamed from the old "HR" role; same permissions
	RoleExecutive Role = "executive"
	RoleEmployee  Role = "employee"
)

type Organization struct {
	ID        string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	Name      string    `gorm:"column:name;size:255;not null" json:"name"`
	Slug      string    `gorm:"column:slug;size:100;not null;uniqueIndex:uq_organizations_slug" json:"slug"`
	CreatedAt time.Time `gorm:"column:created_at" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"-"`

	// JoinCode is a plaintext, canonically-uppercase, redistributable identifier (not a
	// secret by itself — CompanyPasswordHash is the optional secret layer in front of it)
	// that self-service employees enter at /join to reach this org's registration form.
	// default:'' lets AutoMigrate add this NOT NULL column to a table that already has rows
	// (Postgres requires a default to backfill existing rows) — database.backfillJoinCodes
	// then replaces any '' with a real generated code right after migration.
	JoinCode string `gorm:"column:join_code;size:6;not null;default:'';uniqueIndex:uq_organizations_join_code" json:"-"`
	// CompanyPasswordHash is bcrypt, nullable — nil means no company-password gate is set.
	CompanyPasswordHash *string `gorm:"column:company_password_hash;size:255" json:"-"`
	// CollectDepartment/CollectTenure toggle which optional fields the self-service
	// registration form asks for; baseline fields (name/position/email/password) are
	// always collected regardless of these flags.
	//
	// Deliberately no `default:` gorm tag here: GORM silently omits a zero-valued field
	// from INSERT whenever its tag declares a default, letting the column's DB-level
	// default win instead — that would turn an explicit CollectDepartment: false back into
	// true. The app always sets both fields explicitly on every insert, so no DB-level
	// default is needed (the historical ADD COLUMN migration that needed one already ran).
	CollectDepartment bool `gorm:"column:collect_department;not null" json:"-"`
	CollectTenure     bool `gorm:"column:collect_tenure;not null" json:"-"`
}

func (Organization) TableName() string { return "organizations" }

type Department struct {
	ID        string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID     string    `gorm:"column:org_id;size:64;not null;index;uniqueIndex:uq_departments_org_name,priority:1" json:"-"`
	Name      string    `gorm:"column:name;size:255;not null;uniqueIndex:uq_departments_org_name,priority:2" json:"name"`
	JoinCode  *string   `gorm:"column:join_code;size:8;uniqueIndex:uq_departments_join_code" json:"-"`
	CreatedAt time.Time `gorm:"column:created_at" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"-"`

	// RespondentCount is derived from survey_responses for the current period, never
	// stored — see analytics package. Not a DB column.
	RespondentCount int64 `gorm:"-" json:"respondentCount"`
}

func (Department) TableName() string { return "departments" }

type Position struct {
	ID        string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID     string    `gorm:"column:org_id;size:64;not null;index;uniqueIndex:uq_positions_org_name,priority:1" json:"-"`
	Name      string    `gorm:"column:name;size:255;not null;uniqueIndex:uq_positions_org_name,priority:2" json:"name"`
	CreatedAt time.Time `gorm:"column:created_at" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"-"`
}

func (Position) TableName() string { return "positions" }

// User. FullName/LastLoginAt/NotifyNewRound/NotifyMonthlySummary are additive columns beyond
// the given schema (needed by the existing Settings screen) — everything else mirrors the
// supplied users table exactly.
type User struct {
	ID    string `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID string `gorm:"column:org_id;size:64;not null;index" json:"-"`
	// Email is globally unique (not per-org) so login can resolve org purely from email —
	// no company picker needed. This means the same email cannot independently register at
	// two different orgs; acceptable since these represent distinct real people in practice.
	Email        string  `gorm:"column:email;size:255;not null;uniqueIndex:uq_users_email" json:"email"`
	PasswordHash string  `gorm:"column:password_hash;size:255;not null" json:"-"`
	Role         Role    `gorm:"column:role;type:user_role;not null;default:employee;index" json:"role"`
	DepartmentID *string `gorm:"column:department_id;size:64;index" json:"-"`
	PositionID   *string `gorm:"column:position_id;size:64;index" json:"-"`
	IsActive     bool    `gorm:"column:is_active;not null;default:true" json:"-"`

	FullName             string    `gorm:"column:full_name;size:255;not null" json:"fullName"`
	LastLoginAt          time.Time `gorm:"column:last_login_at" json:"lastLoginAt"`
	NotifyNewRound       bool      `gorm:"column:notify_new_round;default:true" json:"notifyNewRound"`
	NotifyMonthlySummary bool      `gorm:"column:notify_monthly_summary;default:true" json:"notifyMonthlySummary"`
	// TenureBucket is set only when the org's CollectTenure toggle was on at registration
	// time; nil for seeded/legacy users and for orgs that don't collect it.
	TenureBucket *string `gorm:"column:tenure_bucket;type:tenure_bucket" json:"-"`

	CreatedAt time.Time `gorm:"column:created_at" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"-"`
}

func (User) TableName() string { return "users" }

// Topic is a small additive taxonomy lookup — not part of the given schema, but needed so
// response_analysis.categories values are drawn from a known vocabulary and the HR heatmap
// columns / Executive radar axes (the app's 6 satisfaction dimensions) keep working under
// the fixed satisfaction_score+comment_text survey model.
type Topic struct {
	ID        string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	Label     Localized `gorm:"column:label;serializer:json" json:"label"`
	SortOrder int       `gorm:"column:sort_order;default:0" json:"-"`
}

func (Topic) TableName() string { return "topics" }

type SurveyPeriod struct {
	ID       string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID    string    `gorm:"column:org_id;size:64;not null;index;uniqueIndex:uq_survey_periods_org_month_year,priority:1" json:"-"`
	Month    int16     `gorm:"column:month;not null;check:month BETWEEN 1 AND 12;uniqueIndex:uq_survey_periods_org_month_year,priority:2" json:"month"`
	Year     int16     `gorm:"column:year;not null;check:year BETWEEN 2000 AND 2100;uniqueIndex:uq_survey_periods_org_month_year,priority:3" json:"year"`
	OpensAt  time.Time `gorm:"column:opens_at;not null" json:"opensAt"`
	ClosesAt time.Time `gorm:"column:closes_at;not null;check:closes_at > opens_at" json:"closesAt"`

	// EnabledExtraQuestions is a subset of dto.ExtraQuestionCatalog's keys — the fixed,
	// optional questions HR chose to turn on for this specific round, on top of the
	// always-present satisfaction score + comment. No `default:` gorm tag (deliberately —
	// see the GORM zero-value footgun documented in WIKI-Backend.md section 13): a nil/empty
	// slice here is a real, meaningful value ("no extra questions this round"), not a
	// placeholder to fall back away from.
	EnabledExtraQuestions []string `gorm:"column:enabled_extra_questions;serializer:json" json:"-"`

	CreatedAt time.Time `gorm:"column:created_at" json:"-"`
}

func (SurveyPeriod) TableName() string { return "survey_periods" }

// SurveySubmission answers only "did this user submit this period?" — for response-rate
// counting and blocking a repeat submission. It shares no key with SurveyResponse, so no
// ordinary query can join a person to the content of their answer.
type SurveySubmission struct {
	ID          string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID       string    `gorm:"column:org_id;size:64;not null;index" json:"-"`
	UserID      string    `gorm:"column:user_id;size:64;not null;uniqueIndex:uq_submissions_user_period,priority:1" json:"-"`
	PeriodID    string    `gorm:"column:period_id;size:64;not null;index;uniqueIndex:uq_submissions_user_period,priority:2" json:"-"`
	SubmittedAt time.Time `gorm:"column:submitted_at;not null" json:"submittedAt"`
}

func (SurveySubmission) TableName() string { return "survey_submissions" }

// SurveyResponse is deliberately NOT linked to users — anonymity comes from the absence of
// a user FK, not from masking. DepartmentID/PositionID are coarse snapshots captured at
// submission time purely so aggregates can be sliced, and every such slice is gated by the
// n<5 rule (see privacy.Suppress / the analytics package's HAVING clauses).
type SurveyResponse struct {
	ID                string  `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID             string  `gorm:"column:org_id;size:64;not null;index" json:"-"`
	PeriodID          string  `gorm:"column:period_id;size:64;not null;index" json:"-"`
	DepartmentID      *string `gorm:"column:department_id;size:64;index" json:"-"`
	PositionID        *string `gorm:"column:position_id;size:64;index" json:"-"`
	SatisfactionScore int16   `gorm:"column:satisfaction_score;not null;check:satisfaction_score BETWEEN 1 AND 5" json:"satisfactionScore"`
	// CommentText holds the open-ended comment AFTER PII redaction — raw text is never
	// persisted (see privacy.Redact, called before this struct is built).
	CommentText string `gorm:"column:comment_text;type:text" json:"commentText,omitempty"`

	SubmittedAt time.Time `gorm:"column:submitted_at;not null;index" json:"submittedAt"`
}

func (SurveyResponse) TableName() string { return "survey_responses" }

// SurveyImport records a completed workbook so the same file cannot be imported twice
// into one survey period. It contains no employee or response identifiers.
type SurveyImport struct {
	ID        string    `gorm:"column:id;primaryKey;size:64"`
	OrgID     string    `gorm:"column:org_id;size:64;not null;uniqueIndex:uq_survey_import_file,priority:1"`
	PeriodID  string    `gorm:"column:period_id;size:64;not null;uniqueIndex:uq_survey_import_file,priority:2"`
	FileHash  string    `gorm:"column:file_hash;size:64;not null;uniqueIndex:uq_survey_import_file,priority:3"`
	RowCount  int       `gorm:"column:row_count;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (SurveyImport) TableName() string { return "survey_imports" }

type ResponseAnalysis struct {
	ID             string  `gorm:"column:id;primaryKey;size:64" json:"id"`
	ResponseID     string  `gorm:"column:response_id;size:64;not null;uniqueIndex" json:"-"`
	SentimentLabel string  `gorm:"column:sentiment_label;type:sentiment_label;not null" json:"sentimentLabel"`
	SentimentScore float32 `gorm:"column:sentiment_score;not null" json:"sentimentScore"`
	Confidence     float32 `gorm:"column:confidence;not null" json:"confidence"`
	LowConfidence  bool    `gorm:"column:low_confidence;not null;default:false" json:"lowConfidence"`
	// Categories are LLM-assigned topic tags drawn from the Topic taxonomy (e.g. ["benefits","manager"]),
	// used both as the heatmap/radar's per-topic slicing key and as word-cloud source frequency.
	// No DB-level GIN index tag here (would require the column to be a genuine jsonb type,
	// which GORM's serializer-based columns don't reliably guarantee) — containment queries
	// cast at query time instead, same pattern as FeedPost.Hashtags in handlers/feed.go.
	Categories []string `gorm:"column:categories;serializer:json" json:"categories"`
	Reason     string   `gorm:"column:reason;type:text" json:"reason,omitempty"`

	AnalyzedAt time.Time `gorm:"column:analyzed_at;not null" json:"analyzedAt"`
}

func (ResponseAnalysis) TableName() string { return "response_analysis" }

// ExtraAnswer stores one answer to one of the fixed, optional questions in
// dto.ExtraQuestionCatalog, attached to a SurveyResponse. Same anonymity guarantee as
// SurveyResponse itself: only a response id, never a user id.
type ExtraAnswer struct {
	ID          string `gorm:"column:id;primaryKey;size:64" json:"id"`
	ResponseID  string `gorm:"column:response_id;size:64;not null;index" json:"-"`
	QuestionKey string `gorm:"column:question_key;size:32;not null;index" json:"-"`
	Value       int16  `gorm:"column:value;not null" json:"-"`
}

func (ExtraAnswer) TableName() string { return "extra_answers" }

// DashboardMetrics/PositionScore/KeywordMonthly exist for schema fidelity with the supplied
// SQL, but this pass computes everything live via analytics queries (same architecture as
// before) rather than writing through a batch job — these tables are created but unused for
// now, ready for a future precompute pass.

type DashboardMetrics struct {
	ID              string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID           string    `gorm:"column:org_id;size:64;not null;index;uniqueIndex:uq_dashboard_metrics_org_period,priority:1" json:"-"`
	PeriodID        string    `gorm:"column:period_id;size:64;not null;uniqueIndex:uq_dashboard_metrics_org_period,priority:2" json:"-"`
	AvgSatisfaction float32   `gorm:"column:avg_satisfaction" json:"avgSatisfaction"`
	ResponseRate    float32   `gorm:"column:response_rate" json:"responseRate"`
	TotalEmployees  int       `gorm:"column:total_employees;not null;default:0" json:"totalEmployees"`
	TotalResponses  int       `gorm:"column:total_responses;not null;default:0" json:"totalResponses"`
	PositivePct     float32   `gorm:"column:positive_pct" json:"positivePct"`
	NeutralPct      float32   `gorm:"column:neutral_pct" json:"neutralPct"`
	NegativePct     float32   `gorm:"column:negative_pct" json:"negativePct"`
	ComputedAt      time.Time `gorm:"column:computed_at" json:"computedAt"`
}

func (DashboardMetrics) TableName() string { return "dashboard_metrics" }

type PositionScore struct {
	ID              string  `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID           string  `gorm:"column:org_id;size:64;not null;index;uniqueIndex:uq_position_scores_org_period_position,priority:1" json:"-"`
	PeriodID        string  `gorm:"column:period_id;size:64;not null;uniqueIndex:uq_position_scores_org_period_position,priority:2" json:"-"`
	PositionID      string  `gorm:"column:position_id;size:64;not null;uniqueIndex:uq_position_scores_org_period_position,priority:3" json:"-"`
	AvgSatisfaction float32 `gorm:"column:avg_satisfaction" json:"avgSatisfaction"`
	ResponseCount   int     `gorm:"column:response_count;not null;default:0" json:"responseCount"`
}

func (PositionScore) TableName() string { return "position_scores" }

type KeywordMonthly struct {
	ID        string `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID     string `gorm:"column:org_id;size:64;not null;index;uniqueIndex:uq_keywords_org_period_keyword,priority:1" json:"-"`
	PeriodID  string `gorm:"column:period_id;size:64;not null;uniqueIndex:uq_keywords_org_period_keyword,priority:2" json:"-"`
	Keyword   string `gorm:"column:keyword;size:100;not null;uniqueIndex:uq_keywords_org_period_keyword,priority:3" json:"keyword"`
	Frequency int    `gorm:"column:frequency;not null;default:1" json:"frequency"`
}

func (KeywordMonthly) TableName() string { return "keywords_monthly" }

type AlertType string

const (
	AlertLowSatisfactionPosition   AlertType = "low_satisfaction_position"
	AlertLowSatisfactionDepartment AlertType = "low_satisfaction_department"
	AlertLowResponseRate           AlertType = "low_response_rate"
	AlertSentimentDrop             AlertType = "sentiment_drop"
)

type AlertSeverity string

const (
	SeverityInfo     AlertSeverity = "info"
	SeverityWarning  AlertSeverity = "warning"
	SeverityCritical AlertSeverity = "critical"
)

type Alert struct {
	ID                  string        `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID               string        `gorm:"column:org_id;size:64;not null;index" json:"-"`
	PeriodID            string        `gorm:"column:period_id;size:64;not null;index" json:"-"`
	AlertType           AlertType     `gorm:"column:alert_type;type:alert_type;not null" json:"alertType"`
	Severity            AlertSeverity `gorm:"column:severity;type:alert_severity;not null;default:warning" json:"severity"`
	Message             string        `gorm:"column:message;type:text;not null" json:"message"`
	RelatedDepartmentID *string       `gorm:"column:related_department_id;size:64" json:"relatedDepartmentId,omitempty"`
	RelatedPositionID   *string       `gorm:"column:related_position_id;size:64" json:"relatedPositionId,omitempty"`
	CreatedAt           time.Time     `gorm:"column:created_at" json:"createdAt"`
	ResolvedAt          *time.Time    `gorm:"column:resolved_at" json:"resolvedAt,omitempty"`
}

func (Alert) TableName() string { return "alerts" }

// KnowledgeBaseSummary is created for schema fidelity; nothing writes to it this pass
// (no RAG/Q&A pipeline yet).
type KnowledgeBaseSummary struct {
	ID          string    `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID       string    `gorm:"column:org_id;size:64;not null;index" json:"-"`
	PeriodID    string    `gorm:"column:period_id;size:64;not null;index" json:"-"`
	SummaryText string    `gorm:"column:summary_text;type:text;not null" json:"summaryText"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (KnowledgeBaseSummary) TableName() string { return "knowledge_base_summaries" }

// ============================================================
// Additive tables — not in the user's supplied schema, needed to keep the rest of the
// existing UI (feed, action items, AI insight panel, topic drilldown) working. All hang off
// OrgID + PeriodID instead of the old ad-hoc period-month string.
// ============================================================

// FeedPost renders publicly only when OptedIn (author consent) AND Published (passed
// moderation) are both true — the two-gate flow the frontend also enforces.
type FeedPost struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	OrgID     string    `gorm:"size:64;index" json:"-"`
	Text      string    `gorm:"type:text;not null" json:"text"`
	Hashtags  []string  `gorm:"serializer:json" json:"hashtags"`
	CreatedAt time.Time `json:"-"`
	PostedOn  string    `gorm:"size:32" json:"createdAt"`
	Upvotes   int       `gorm:"default:0" json:"upvotes"`
	Downvotes int       `gorm:"default:0" json:"downvotes"`
	HRReplied bool      `gorm:"default:false" json:"hrReplied"`
	OptedIn   bool      `gorm:"default:false;index" json:"optedIn"`
	Published bool      `gorm:"default:false;index" json:"published"`
}

// FeedVote records that *a* vote happened for dedupe, keyed by a per-user-per-post hash
// rather than a plain user id, so the vote table cannot be used to profile a user's opinions.
type FeedVote struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	PostID    string    `gorm:"size:64;index:idx_post_voter,unique" json:"-"`
	VoterHash string    `gorm:"size:64;index:idx_post_voter,unique" json:"-"`
	Direction int       `json:"-"` // +1 or -1
	CreatedAt time.Time `json:"-"`
}

type ActionItem struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	OrgID      string    `gorm:"size:64;index" json:"-"`
	Topic      Localized `gorm:"serializer:json" json:"topic"`
	TopicID    string    `gorm:"size:64;index" json:"-"`
	Assignee   string    `gorm:"size:255" json:"assignee"`
	Status     string    `gorm:"size:32;default:in_progress" json:"status"`
	CreatedBy  Role      `gorm:"size:32" json:"createdBy"`
	Level      string    `gorm:"size:32;default:full" json:"level"`
	TargetDate string    `gorm:"size:32" json:"targetDate"`
	CreatedAt  time.Time `json:"-"`
}

type PublishedSummary struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	OrgID       string    `gorm:"size:64;index" json:"-"`
	Title       Localized `gorm:"serializer:json" json:"title"`
	Body        Localized `gorm:"serializer:json" json:"body"`
	PublishedAt string    `gorm:"size:32" json:"publishedAt"`
}

// AIInsight stores the period summary derived from AI-analyzed responses. The dashboard
// refreshes this row on demand so an open period stays current as responses arrive.
type AIInsight struct {
	ID              uint              `gorm:"primaryKey" json:"-"`
	OrgID           string            `gorm:"size:64;index" json:"-"`
	PeriodID        string            `gorm:"size:64;index" json:"-"`
	OverallScore    int               `json:"overallScore"`
	Confidence      string            `gorm:"size:16" json:"confidence"`
	Summary         Localized         `gorm:"serializer:json" json:"summary"`
	IssueConfidence []IssueConfidence `gorm:"serializer:json" json:"issueConfidence"`
}

type IssueConfidence struct {
	Label      Localized `json:"label"`
	Confidence int       `json:"confidence"`
}

type UrgentIssue struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	OrgID          string    `gorm:"size:64;index" json:"-"`
	PeriodID       string    `gorm:"size:64;index" json:"-"`
	Label          Localized `gorm:"serializer:json" json:"label"`
	DepartmentName Localized `gorm:"serializer:json" json:"departmentName"`
	DepartmentID   string    `gorm:"size:64" json:"-"`
	Rank           int       `json:"-"`
}

type DecisionItem struct {
	ID       string    `gorm:"primaryKey;size:64" json:"id"`
	OrgID    string    `gorm:"size:64;index" json:"-"`
	PeriodID string    `gorm:"size:64;index" json:"-"`
	Rank     int       `json:"rank"`
	Label    Localized `gorm:"serializer:json" json:"label"`
	Severity int       `json:"severity"`
}

type WordCloudTerm struct {
	ID        uint      `gorm:"primaryKey" json:"-"`
	OrgID     string    `gorm:"size:64;index" json:"-"`
	PeriodID  string    `gorm:"size:64;index" json:"-"`
	Term      Localized `gorm:"serializer:json" json:"term"`
	Frequency int       `json:"frequency"`
	TopicID   string    `gorm:"size:64" json:"topicId"`
}

type TopicSubIssue struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	TopicID    string    `gorm:"size:64;index" json:"-"`
	Label      Localized `gorm:"serializer:json" json:"label"`
	Percentage int       `json:"percentage"`
}

// TopicSampleQuote holds only text that has already passed PII redaction.
type TopicSampleQuote struct {
	ID      uint   `gorm:"primaryKey" json:"-"`
	TopicID string `gorm:"size:64;index" json:"-"`
	Text    string `gorm:"type:text" json:"text"`
}

// RefreshToken is stored hashed so a database leak cannot be replayed as a session.
type RefreshToken struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    string    `gorm:"size:64;index"`
	TokenHash string    `gorm:"size:128;uniqueIndex"`
	ExpiresAt time.Time `gorm:"index"`
	RevokedAt *time.Time
	CreatedAt time.Time
}

// AllModels is the AutoMigrate list, ordered parents-before-children so GORM can create FK
// constraints; keep new models registered here in dependency order.
func AllModels() []any {
	return []any{
		&Organization{},
		&Department{}, &Position{},
		&User{},
		&Topic{},
		&SurveyPeriod{},
		&SurveySubmission{}, &SurveyResponse{}, &SurveyImport{},
		&ResponseAnalysis{}, &ExtraAnswer{},
		&DashboardMetrics{}, &PositionScore{}, &KeywordMonthly{},
		&Alert{}, &KnowledgeBaseSummary{},
		&FeedPost{}, &FeedVote{},
		&ActionItem{}, &PublishedSummary{},
		&AIInsight{}, &UrgentIssue{}, &DecisionItem{}, &WordCloudTerm{},
		&TopicSubIssue{}, &TopicSampleQuote{},
		&RefreshToken{},
	}
}
