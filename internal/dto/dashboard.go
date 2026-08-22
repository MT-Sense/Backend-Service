package dto

import (
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/models"
)

// --- reference lists ---

type Topic struct {
	ID    string    `json:"id"`
	Label Localized `json:"label"`
}

func NewTopics(topics []models.Topic) []Topic {
	out := make([]Topic, 0, len(topics))
	for _, t := range topics {
		out = append(out, Topic{ID: t.ID, Label: t.Label})
	}
	return out
}

type Department struct {
	ID              string    `json:"id"`
	Name            Localized `json:"name"`
	RespondentCount int64     `json:"respondentCount"`
}

func NewDepartments(departments []models.Department, counts map[string]int64) []Department {
	out := make([]Department, 0, len(departments))
	for _, d := range departments {
		out = append(out, Department{ID: d.ID, Name: d.Name, RespondentCount: counts[d.ID]})
	}
	return out
}

// --- HR KPIs ---

type EnpsKpi struct {
	Value            int `json:"value"`
	DeltaVsLastMonth int `json:"deltaVsLastMonth"`
}

type SatisfactionKpi struct {
	Value float64 `json:"value"`
	Trend string  `json:"trend"` // up | down | flat
}

type BurnoutKpi struct {
	Percentage        int `json:"percentage"`
	DepartmentsAtRisk int `json:"departmentsAtRisk"`
}

type ResponseRateKpi struct {
	Percentage int   `json:"percentage"`
	Responded  int64 `json:"responded"`
	Total      int64 `json:"total"`
}

type TrendPoint struct {
	Month        string  `json:"month"`
	Enps         int     `json:"enps"`
	Satisfaction float64 `json:"satisfaction"`
}

// HrKpis mirrors the frontend's HrKpis type.
type HrKpis struct {
	Enps         EnpsKpi         `json:"enps"`
	Satisfaction SatisfactionKpi `json:"satisfaction"`
	BurnoutRisk  BurnoutKpi      `json:"burnoutRisk"`
	ResponseRate ResponseRateKpi `json:"responseRate"`
	Sentiment    Sentiment       `json:"sentiment"`
	Trend        []TrendPoint    `json:"trend"`
}

func NewTrend(points []analytics.TrendPoint) []TrendPoint {
	out := make([]TrendPoint, 0, len(points))
	for _, p := range points {
		out = append(out, TrendPoint{Month: p.Month, Enps: p.ENPS, Satisfaction: p.Satisfaction})
	}
	return out
}

// --- heatmap ---

type HeatmapCell struct {
	TopicID string                 `json:"topicId"`
	Score   Suppressible[float64] `json:"score"`
}

type HeatmapRow struct {
	Department Department    `json:"department"`
	Cells      []HeatmapCell `json:"cells"`
}

type Heatmap struct {
	Topics []Topic      `json:"topics"`
	Rows   []HeatmapRow `json:"rows"`
}

// --- AI insight ---

type IssueConfidence struct {
	Label      Localized `json:"label"`
	Confidence int       `json:"confidence"`
}

type AiInsight struct {
	OverallScore    int               `json:"overallScore"`
	Confidence      string            `json:"confidence"`
	Summary         Localized         `json:"summary"`
	IssueConfidence []IssueConfidence `json:"issueConfidence"`
}

type UrgentIssue struct {
	ID             string    `json:"id"`
	Label          Localized `json:"label"`
	DepartmentName Localized `json:"departmentName"`
}

type InsightResponse struct {
	Insight      AiInsight     `json:"insight"`
	UrgentIssues []UrgentIssue `json:"urgentIssues"`
}

func NewInsight(insight *models.AIInsight, urgent []models.UrgentIssue) InsightResponse {
	issues := make([]IssueConfidence, 0, len(insight.IssueConfidence))
	for _, i := range insight.IssueConfidence {
		issues = append(issues, IssueConfidence{Label: i.Label, Confidence: i.Confidence})
	}
	out := InsightResponse{
		Insight: AiInsight{
			OverallScore:    insight.OverallScore,
			Confidence:      insight.Confidence,
			Summary:         insight.Summary,
			IssueConfidence: issues,
		},
		UrgentIssues: make([]UrgentIssue, 0, len(urgent)),
	}
	for _, u := range urgent {
		out.UrgentIssues = append(out.UrgentIssues, UrgentIssue{
			ID:             u.ID,
			Label:          u.Label,
			DepartmentName: u.DepartmentName,
		})
	}
	return out
}

type WordCloudTerm struct {
	Term      Localized `json:"term"`
	Frequency int       `json:"frequency"`
	TopicID   string    `json:"topicId"`
}

func NewWordCloud(terms []models.WordCloudTerm) []WordCloudTerm {
	out := make([]WordCloudTerm, 0, len(terms))
	for _, t := range terms {
		out = append(out, WordCloudTerm{Term: t.Term, Frequency: t.Frequency, TopicID: t.TopicID})
	}
	return out
}

// --- topic drill-down (HR only) ---

type TopicTrendPoint struct {
	Month string  `json:"month"`
	Score float64 `json:"score"`
}

type TopicSubIssue struct {
	ID         string    `json:"id"`
	Label      Localized `json:"label"`
	Percentage int       `json:"percentage"`
}

// TopicDrilldown is the only response carrying respondent text. SampleQuotes holds strings
// that were redacted before they were ever stored, and the route is HR-gated.
type TopicDrilldown struct {
	TopicID          string            `json:"topicId"`
	Label            Localized         `json:"label"`
	Score            float64           `json:"score"`
	CompanyAverage   float64           `json:"companyAverage"`
	RespondentCount  int64             `json:"respondentCount"`
	PercentageTagged int               `json:"percentageTagged"`
	Trend            []TopicTrendPoint `json:"trend"`
	SubIssues        []TopicSubIssue   `json:"subIssues"`
	Sentiment        Sentiment         `json:"sentiment"`
	SampleQuotes     []string          `json:"sampleQuotes"`
}

func NewTopicTrend(points []analytics.TopicTrendPoint) []TopicTrendPoint {
	out := make([]TopicTrendPoint, 0, len(points))
	for _, p := range points {
		out = append(out, TopicTrendPoint{Month: p.Month, Score: p.Score})
	}
	return out
}

func NewSubIssues(issues []models.TopicSubIssue) []TopicSubIssue {
	out := make([]TopicSubIssue, 0, len(issues))
	for _, i := range issues {
		out = append(out, TopicSubIssue{ID: i.ID, Label: i.Label, Percentage: i.Percentage})
	}
	return out
}

// --- executive summary ---

type RadarAxis struct {
	TopicID   string  `json:"topicId"`
	ThisMonth float64 `json:"thisMonth"`
	LastMonth float64 `json:"lastMonth"`
}

type DepartmentScore struct {
	DepartmentID string                `json:"departmentId"`
	Score        Suppressible[float64] `json:"score"`
}

type TenureScore struct {
	Bucket string  `json:"bucket"`
	Score  float64 `json:"score"`
}

type DecisionItem struct {
	ID       string    `json:"id"`
	Rank     int       `json:"rank"`
	Label    Localized `json:"label"`
	Severity int       `json:"severity"`
}

// ExecutiveSummary is aggregate-only by construction: there is no field on this struct
// that could carry an individual's words, so the Executive restriction holds at the type
// level and not merely by handler discipline.
type ExecutiveSummary struct {
	Score                int               `json:"score"`
	Sentiment            Sentiment         `json:"sentiment"`
	Radar                []RadarAxis       `json:"radar"`
	DepartmentComparison []DepartmentScore `json:"departmentComparison"`
	TenureComparison     []TenureScore     `json:"tenureComparison"`
	DecisionItems        []DecisionItem    `json:"decisionItems"`
}

func NewRadar(axes []analytics.RadarAxis) []RadarAxis {
	out := make([]RadarAxis, 0, len(axes))
	for _, a := range axes {
		out = append(out, RadarAxis{TopicID: a.TopicID, ThisMonth: a.ThisMonth, LastMonth: a.LastMonth})
	}
	return out
}

func NewDepartmentScores(scores []analytics.DepartmentScore) []DepartmentScore {
	out := make([]DepartmentScore, 0, len(scores))
	for _, s := range scores {
		out = append(out, DepartmentScore{DepartmentID: s.DepartmentID, Score: s.Score})
	}
	return out
}

func NewTenureScores(scores []analytics.TenureScore) []TenureScore {
	out := make([]TenureScore, 0, len(scores))
	for _, s := range scores {
		out = append(out, TenureScore{Bucket: s.Bucket, Score: s.Score})
	}
	return out
}

func NewDecisionItems(items []models.DecisionItem) []DecisionItem {
	out := make([]DecisionItem, 0, len(items))
	for _, i := range items {
		out = append(out, DecisionItem{ID: i.ID, Rank: i.Rank, Label: i.Label, Severity: i.Severity})
	}
	return out
}
