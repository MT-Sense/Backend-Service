package models

import (
	"encoding/json"
	"time"
)

// Localized mirrors the frontend's LocalizedText ({ th, en }). Stored as JSON in one column.
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

func Visible[T any](data T) Suppressible[T]  { return Suppressible[T]{Suppressed: false, Data: data} }
func Hidden[T any]() Suppressible[T]         { return Suppressible[T]{Suppressed: true} }

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

type Role string

const (
	RoleHR        Role = "HR"
	RoleExecutive Role = "Executive"
	RoleEmployee  Role = "Employee"
)

type User struct {
	ID                   string    `gorm:"primaryKey;size:64" json:"id"`
	Email                string    `gorm:"uniqueIndex;size:255;not null" json:"email"`
	PasswordHash         string    `gorm:"size:255;not null" json:"-"`
	FullName             string    `gorm:"size:255;not null" json:"fullName"`
	Role                 Role      `gorm:"size:32;not null;index" json:"role"`
	DepartmentID         string    `gorm:"size:64;index" json:"-"`
	TenureBucket         string    `gorm:"size:16" json:"-"`
	LastLoginAt          time.Time `json:"lastLoginAt"`
	NotifyNewRound       bool      `gorm:"default:true" json:"notifyNewRound"`
	NotifyMonthlySummary bool      `gorm:"default:true" json:"notifyMonthlySummary"`
	CreatedAt            time.Time `json:"-"`
	UpdatedAt            time.Time `json:"-"`
}

type Department struct {
	ID   string    `gorm:"primaryKey;size:64" json:"id"`
	Name Localized `gorm:"serializer:json" json:"name"`
	// RespondentCount is derived from survey_responses, never stored — see dashboard handlers.
	RespondentCount int64 `gorm:"-" json:"respondentCount"`
}

// Topic is the single source of truth for the 6 satisfaction dimensions shared by the
// HR heatmap columns and the Executive radar axes.
type Topic struct {
	ID       string    `gorm:"primaryKey;size:64" json:"id"`
	Label    Localized `gorm:"serializer:json" json:"label"`
	SortOrder int      `gorm:"default:0" json:"-"`
}

type QuestionType string

const (
	QuestionScale5       QuestionType = "scale5"
	QuestionENPS         QuestionType = "enps"
	QuestionSingleChoice QuestionType = "singleChoice"
	QuestionMultiChoice  QuestionType = "multiChoice"
	QuestionOpenText     QuestionType = "openText"
)

type Survey struct {
	ID            string       `gorm:"primaryKey;size:64" json:"id"`
	Title         Localized    `gorm:"serializer:json" json:"title"`
	Cadence       string       `gorm:"size:32" json:"cadence"`
	NextRoundDate string       `gorm:"size:32" json:"nextRoundDate"`
	Status        string       `gorm:"size:32;default:draft" json:"status"`
	Steps         []SurveyStep `gorm:"foreignKey:SurveyID;constraint:OnDelete:CASCADE" json:"steps"`
	CreatedAt     time.Time    `json:"-"`
	UpdatedAt     time.Time    `json:"-"`
}

type SurveyStep struct {
	ID               string           `gorm:"primaryKey;size:64" json:"id"`
	SurveyID         string           `gorm:"size:64;index" json:"-"`
	Title            Localized        `gorm:"serializer:json" json:"title"`
	EstimatedMinutes int              `json:"estimatedMinutes"`
	SortOrder        int              `gorm:"default:0" json:"-"`
	Questions        []SurveyQuestion `gorm:"foreignKey:StepID;constraint:OnDelete:CASCADE" json:"questions"`
}

type SurveyQuestion struct {
	ID             string       `gorm:"primaryKey;size:64" json:"id"`
	StepID         string       `gorm:"size:64;index" json:"-"`
	Type           QuestionType `gorm:"size:32;not null" json:"type"`
	Text           Localized    `gorm:"serializer:json" json:"text"`
	Required       bool         `json:"required"`
	MetricMapping  string       `gorm:"size:64" json:"metricMapping,omitempty"`
	TopicID        string       `gorm:"size:64;index" json:"-"`
	SendToAI       bool         `json:"sendToAi,omitempty"`
	Options        []Localized  `gorm:"serializer:json" json:"options,omitempty"`
	TagSuggestions []string     `gorm:"serializer:json" json:"tagSuggestions,omitempty"`
	AllowPublish   bool         `json:"allowPublishOptIn,omitempty"`
	SortOrder      int          `gorm:"default:0" json:"-"`
}

// SurveyResponse deliberately carries NO foreign key to users. Identity is replaced by a
// random per-submission token; DepartmentID and TenureBucket are coarse attributes captured
// at submission time purely so aggregates can be sliced — and every such slice is gated by
// the n<5 rule, which is what stops those attributes from re-identifying anyone.
type SurveyResponse struct {
	ID              string           `gorm:"primaryKey;size:64" json:"id"`
	SurveyID        string           `gorm:"size:64;index" json:"surveyId"`
	AnonymousToken  string           `gorm:"size:64;uniqueIndex;not null" json:"anonymousToken"`
	DepartmentID    string           `gorm:"size:64;index" json:"-"`
	TenureBucket    string           `gorm:"size:16;index" json:"-"`
	SubmittedAt     time.Time        `gorm:"index" json:"submittedAt"`
	PeriodMonth     string           `gorm:"size:7;index" json:"-"` // YYYY-MM, for period filters
	Answers         []ResponseAnswer `gorm:"foreignKey:ResponseID;constraint:OnDelete:CASCADE" json:"answers"`
}

type ResponseAnswer struct {
	ID         uint            `gorm:"primaryKey" json:"-"`
	ResponseID string          `gorm:"size:64;index" json:"-"`
	QuestionID string          `gorm:"size:64;index" json:"questionId"`
	TopicID    string          `gorm:"size:64;index" json:"-"`
	// NumericValue is set for scale5 / enps so aggregation is plain SQL rather than JSON parsing.
	NumericValue *float64        `gorm:"index" json:"-"`
	Value        json.RawMessage `gorm:"serializer:json" json:"value"`
	// TextRedacted holds open text AFTER PII redaction. Raw text is never persisted.
	TextRedacted   string   `gorm:"type:text" json:"-"`
	Sentiment      string   `gorm:"size:16;index" json:"-"`
	Tags           []string `gorm:"serializer:json" json:"tags,omitempty"`
	OptedInToFeed  bool     `json:"optedInToFeed,omitempty"`
}

// FeedPost renders publicly only when OptedIn (author consent) AND Published (passed
// moderation) are both true — the two-gate flow the frontend also enforces.
type FeedPost struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	Text       string    `gorm:"type:text;not null" json:"text"`
	Hashtags   []string  `gorm:"serializer:json" json:"hashtags"`
	CreatedAt  time.Time `json:"-"`
	PostedOn   string    `gorm:"size:32" json:"createdAt"`
	Upvotes    int       `gorm:"default:0" json:"upvotes"`
	Downvotes  int       `gorm:"default:0" json:"downvotes"`
	HRReplied  bool      `gorm:"default:false" json:"hrReplied"`
	OptedIn    bool      `gorm:"default:false;index" json:"optedIn"`
	Published  bool      `gorm:"default:false;index" json:"published"`
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
	Title       Localized `gorm:"serializer:json" json:"title"`
	Body        Localized `gorm:"serializer:json" json:"body"`
	PublishedAt string    `gorm:"size:32" json:"publishedAt"`
}

// AuditSubmissionLog answers "did this user submit?" for reminder emails, and nothing else.
// It is a separate table from SurveyResponse with no shared key, so no ordinary query can
// join a person to the content of their answers.
type AuditSubmissionLog struct {
	ID          uint       `gorm:"primaryKey" json:"-"`
	UserID      string     `gorm:"size:64;index:idx_user_survey,unique" json:"userId"`
	SurveyID    string     `gorm:"size:64;index:idx_user_survey,unique" json:"surveyId"`
	Status      string     `gorm:"size:32;default:not_submitted" json:"status"`
	SubmittedAt *time.Time `json:"submittedAt"`
}

// AIInsight stores the output of the analysis pipeline. Rows are written by that pipeline,
// never computed per-request.
// ponytail: seeded rows stand in for a real LLM pass; swap the seeder for the pipeline writer.
type AIInsight struct {
	ID           uint      `gorm:"primaryKey" json:"-"`
	PeriodMonth  string    `gorm:"size:7;index" json:"-"`
	OverallScore int       `json:"overallScore"`
	Confidence   string    `gorm:"size:16" json:"confidence"`
	Summary      Localized `gorm:"serializer:json" json:"summary"`
	IssueConfidence []IssueConfidence `gorm:"serializer:json" json:"issueConfidence"`
}

type IssueConfidence struct {
	Label      Localized `json:"label"`
	Confidence int       `json:"confidence"`
}

type UrgentIssue struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	PeriodMonth    string    `gorm:"size:7;index" json:"-"`
	Label          Localized `gorm:"serializer:json" json:"label"`
	DepartmentName Localized `gorm:"serializer:json" json:"departmentName"`
	DepartmentID   string    `gorm:"size:64" json:"-"`
	Rank           int       `json:"-"`
}

type DecisionItem struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	PeriodMonth string    `gorm:"size:7;index" json:"-"`
	Rank        int       `json:"rank"`
	Label       Localized `gorm:"serializer:json" json:"label"`
	Severity    int       `json:"severity"`
}

type WordCloudTerm struct {
	ID          uint      `gorm:"primaryKey" json:"-"`
	PeriodMonth string    `gorm:"size:7;index" json:"-"`
	Term        Localized `gorm:"serializer:json" json:"term"`
	Frequency   int       `json:"frequency"`
	TopicID     string    `gorm:"size:64" json:"topicId"`
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

// AllModels is the AutoMigrate list; keep new models registered here.
func AllModels() []any {
	return []any{
		&User{}, &Department{}, &Topic{},
		&Survey{}, &SurveyStep{}, &SurveyQuestion{},
		&SurveyResponse{}, &ResponseAnswer{},
		&FeedPost{}, &FeedVote{},
		&ActionItem{}, &PublishedSummary{},
		&AuditSubmissionLog{},
		&AIInsight{}, &UrgentIssue{}, &DecisionItem{}, &WordCloudTerm{},
		&TopicSubIssue{}, &TopicSampleQuote{},
		&RefreshToken{},
	}
}
