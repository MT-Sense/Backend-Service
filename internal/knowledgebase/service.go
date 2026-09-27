package knowledgebase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

var ErrInsufficientData = errors.New("at least 5 responses are required to compile knowledge")

type Compiler interface {
	CompileKnowledge(context.Context, aiservice.KnowledgeCompileRequest) (*aiservice.KnowledgeCompileResult, error)
}

type Answerer interface {
	AskKnowledge(context.Context, aiservice.KnowledgeQARequest) (*aiservice.KnowledgeQAResult, error)
}

type Service struct {
	db       *gorm.DB
	compiler Compiler
}

func New(db *gorm.DB, compiler Compiler) *Service {
	return &Service{db: db, compiler: compiler}
}

type Article struct {
	Entry       models.KnowledgeBaseSummary
	PeriodLabel string
}

type QASource struct {
	PeriodID    string `json:"periodId"`
	PeriodLabel string `json:"periodLabel"`
	Title       string `json:"title"`
}

type QAResult struct {
	Answer  string     `json:"answer"`
	Sources []QASource `json:"sources"`
}

func periodLabel(year, month int16) string {
	return fmt.Sprintf("%04d-%02d", year, month)
}

func sourceHash(request aiservice.KnowledgeCompileRequest) (string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) buildRequest(orgID, periodID string) (aiservice.KnowledgeCompileRequest, error) {
	var period models.SurveyPeriod
	if err := s.db.Where("org_id = ? AND id = ?", orgID, periodID).First(&period).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}

	var total int64
	if err := s.db.Model(&models.SurveyResponse{}).
		Where("org_id = ? AND period_id = ?", orgID, periodID).
		Count(&total).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}
	if total < privacy.MinGroupSizeSQL {
		return aiservice.KnowledgeCompileRequest{}, ErrInsufficientData
	}

	var avg *float64
	if err := s.db.Model(&models.SurveyResponse{}).
		Where("org_id = ? AND period_id = ?", orgID, periodID).
		Select("AVG(satisfaction_score)").
		Scan(&avg).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}
	if avg == nil {
		return aiservice.KnowledgeCompileRequest{}, ErrInsufficientData
	}

	source := aiservice.KnowledgeSource{
		PeriodID:            period.ID,
		PeriodLabel:         periodLabel(period.Year, period.Month),
		TotalResponses:      total,
		AverageSatisfaction: round(*avg, 2),
		Topics:              []aiservice.KnowledgeTopic{},
		Departments:         []aiservice.KnowledgeDepartment{},
	}

	var sentiment struct {
		Positive int64
		Neutral  int64
		Negative int64
		Total    int64
	}
	if err := s.db.Model(&models.ResponseAnalysis{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_analysis.response_id").
		Where("survey_responses.org_id = ? AND survey_responses.period_id = ?", orgID, periodID).
		Select(`
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'pos') AS positive,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'neu') AS neutral,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'neg') AS negative,
			COUNT(*) AS total`).
		Scan(&sentiment).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}
	if sentiment.Total >= privacy.MinGroupSizeSQL {
		pct := func(value int64) int {
			return int(float64(value)/float64(sentiment.Total)*100 + 0.5)
		}
		positive, neutral, negative := pct(sentiment.Positive), pct(sentiment.Neutral), pct(sentiment.Negative)
		positive += 100 - (positive + neutral + negative)
		source.Sentiment = &aiservice.KnowledgeSentiment{
			Positive: positive,
			Neutral:  neutral,
			Negative: negative,
			Analyzed: int(sentiment.Total),
		}
	}

	var topics []models.Topic
	if err := s.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}
	for _, topic := range topics {
		containsTopic, err := json.Marshal([]string{topic.ID})
		if err != nil {
			return aiservice.KnowledgeCompileRequest{}, err
		}
		var row struct {
			Average *float64
			Count   int64
		}
		if err := s.db.Model(&models.SurveyResponse{}).
			Joins("JOIN response_analysis ON response_analysis.response_id = survey_responses.id").
			Where("survey_responses.org_id = ? AND survey_responses.period_id = ? AND response_analysis.categories::jsonb @> ?::jsonb",
				orgID, periodID, string(containsTopic)).
			Select("AVG(survey_responses.satisfaction_score) AS average, COUNT(DISTINCT survey_responses.id) AS count").
			Scan(&row).Error; err != nil {
			return aiservice.KnowledgeCompileRequest{}, err
		}
		if row.Count < privacy.MinGroupSizeSQL || row.Average == nil {
			continue
		}
		source.Topics = append(source.Topics, aiservice.KnowledgeTopic{
			ID:                  topic.ID,
			Label:               topic.Label.TH,
			AverageSatisfaction: round(*row.Average, 2),
			MentionCount:        row.Count,
		})
	}

	var departments []struct {
		ID      string
		Name    string
		Average float64
		Count   int64
	}
	if err := s.db.Table("survey_responses").
		Select("departments.id, departments.name, AVG(survey_responses.satisfaction_score) AS average, COUNT(*) AS count").
		Joins("JOIN departments ON departments.id = survey_responses.department_id").
		Where("survey_responses.org_id = ? AND survey_responses.period_id = ?", orgID, periodID).
		Group("departments.id, departments.name").
		Having("COUNT(*) >= ?", privacy.MinGroupSizeSQL).
		Order("departments.name").
		Scan(&departments).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}
	for _, department := range departments {
		source.Departments = append(source.Departments, aiservice.KnowledgeDepartment{
			ID:                  department.ID,
			Name:                department.Name,
			AverageSatisfaction: round(department.Average, 2),
			ResponseCount:       department.Count,
		})
	}

	type previousRow struct {
		PeriodID    string
		TitleTH     string
		SummaryText string
		Month       int16
		Year        int16
	}
	var previousRows []previousRow
	if err := s.db.Table("knowledge_base_summaries AS kb").
		Select("kb.period_id, kb.title_th, kb.summary_text, survey_periods.month, survey_periods.year").
		Joins("JOIN survey_periods ON survey_periods.id = kb.period_id").
		Where("kb.org_id = ? AND kb.period_id <> ? AND (survey_periods.year < ? OR (survey_periods.year = ? AND survey_periods.month < ?))",
			orgID, periodID, period.Year, period.Year, period.Month).
		Order("survey_periods.year DESC, survey_periods.month DESC").
		Limit(5).
		Scan(&previousRows).Error; err != nil {
		return aiservice.KnowledgeCompileRequest{}, err
	}
	previous := make([]aiservice.PreviousKnowledgeArticle, 0, len(previousRows))
	for _, row := range previousRows {
		previous = append(previous, aiservice.PreviousKnowledgeArticle{
			PeriodID:    row.PeriodID,
			PeriodLabel: periodLabel(row.Year, row.Month),
			TitleTH:     row.TitleTH,
			SummaryTH:   row.SummaryText,
		})
	}

	return aiservice.KnowledgeCompileRequest{Source: source, PreviousArticles: previous}, nil
}

func (s *Service) CompilePeriod(ctx context.Context, orgID, periodID string, force bool) (*Article, bool, error) {
	request, err := s.buildRequest(orgID, periodID)
	if err != nil {
		return nil, false, err
	}
	hash, err := sourceHash(request)
	if err != nil {
		return nil, false, err
	}
	snapshot, err := json.Marshal(request.Source)
	if err != nil {
		return nil, false, err
	}

	var existing models.KnowledgeBaseSummary
	err = s.db.Where("org_id = ? AND period_id = ?", orgID, periodID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if err == nil && !force && existing.SourceHash == hash && strings.TrimSpace(existing.MarkdownText) != "" {
		return &Article{Entry: existing, PeriodLabel: request.Source.PeriodLabel}, true, nil
	}

	result, err := s.compiler.CompileKnowledge(ctx, request)
	if err != nil {
		return nil, false, err
	}

	allowedRelated := make(map[string]bool, len(request.PreviousArticles))
	for _, previous := range request.PreviousArticles {
		allowedRelated[previous.PeriodID] = true
	}
	related := make([]string, 0, len(result.RelatedPeriodIDs))
	for _, id := range result.RelatedPeriodIDs {
		if allowedRelated[id] {
			related = append(related, id)
		}
	}

	now := time.Now().UTC()
	entry := existing
	if errors.Is(err, gorm.ErrRecordNotFound) || entry.ID == "" {
		entry.ID = uuid.NewString()
		entry.OrgID = orgID
		entry.PeriodID = periodID
		entry.CreatedAt = now
	}
	entry.TitleTH = strings.TrimSpace(result.TitleTH)
	entry.TitleEN = strings.TrimSpace(result.TitleEN)
	entry.SummaryText = strings.TrimSpace(result.SummaryTH)
	entry.SummaryEN = strings.TrimSpace(result.SummaryEN)
	entry.MarkdownText = strings.TrimSpace(result.Markdown)
	entry.Tags = result.Tags
	entry.RelatedPeriodIDs = related
	entry.SuggestedQuestions = result.SuggestedQuestions
	entry.SourceHash = hash
	entry.SourceSnapshot = string(snapshot)
	entry.CompiledAt = now
	entry.UpdatedAt = now

	if existing.ID == "" {
		if err := s.db.Create(&entry).Error; err != nil {
			return nil, false, err
		}
	} else if err := s.db.Save(&entry).Error; err != nil {
		return nil, false, err
	}
	return &Article{Entry: entry, PeriodLabel: request.Source.PeriodLabel}, false, nil
}

func (s *Service) List(orgID string) ([]Article, error) {
	type row struct {
		models.KnowledgeBaseSummary
		Month int16
		Year  int16
	}
	var rows []row
	if err := s.db.Table("knowledge_base_summaries AS kb").
		Select("kb.*, survey_periods.month, survey_periods.year").
		Joins("JOIN survey_periods ON survey_periods.id = kb.period_id").
		Where("kb.org_id = ?", orgID).
		Order("survey_periods.year DESC, survey_periods.month DESC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Article, 0, len(rows))
	for _, row := range rows {
		out = append(out, Article{Entry: row.KnowledgeBaseSummary, PeriodLabel: periodLabel(row.Year, row.Month)})
	}
	return out, nil
}

func (s *Service) Get(orgID, periodID string) (*Article, error) {
	var entry models.KnowledgeBaseSummary
	if err := s.db.Where("org_id = ? AND period_id = ?", orgID, periodID).First(&entry).Error; err != nil {
		return nil, err
	}
	var period models.SurveyPeriod
	if err := s.db.Where("org_id = ? AND id = ?", orgID, periodID).First(&period).Error; err != nil {
		return nil, err
	}
	return &Article{Entry: entry, PeriodLabel: periodLabel(period.Year, period.Month)}, nil
}

func (s *Service) Ask(ctx context.Context, orgID, question, locale string) (*QAResult, error) {
	answerer, ok := s.compiler.(Answerer)
	if !ok {
		return nil, errors.New("knowledge Q&A is not configured")
	}
	articles, err := s.List(orgID)
	if err != nil {
		return nil, err
	}
	if len(articles) > 6 {
		articles = articles[:6]
	}

	request := aiservice.KnowledgeQARequest{Question: strings.TrimSpace(question), Locale: locale}
	request.Articles = make([]aiservice.KnowledgeQAArticle, 0, len(articles))
	labels := make(map[string]QASource, len(articles))
	for _, article := range articles {
		entry := article.Entry
		var snapshot json.RawMessage
		if strings.TrimSpace(entry.SourceSnapshot) != "" && json.Valid([]byte(entry.SourceSnapshot)) {
			snapshot = json.RawMessage(entry.SourceSnapshot)
		}
		title := entry.TitleTH
		summary := entry.SummaryText
		if locale == "en" {
			title = entry.TitleEN
			summary = entry.SummaryEN
		}
		request.Articles = append(request.Articles, aiservice.KnowledgeQAArticle{
			PeriodID:       entry.PeriodID,
			PeriodLabel:    article.PeriodLabel,
			Title:          title,
			Summary:        summary,
			Tags:           entry.Tags,
			SourceSnapshot: snapshot,
		})
		labels[entry.PeriodID] = QASource{PeriodID: entry.PeriodID, PeriodLabel: article.PeriodLabel, Title: title}
	}

	result, err := answerer.AskKnowledge(ctx, request)
	if err != nil {
		return nil, err
	}
	out := &QAResult{Answer: strings.TrimSpace(result.Answer), Sources: []QASource{}}
	for _, id := range result.UsedPeriodIDs {
		if source, exists := labels[id]; exists {
			out.Sources = append(out.Sources, source)
		}
	}
	return out, nil
}

func (s *Service) IndexMarkdown(orgID string) (string, error) {
	articles, err := s.List(orgID)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	builder.WriteString("# MT-Sense Knowledge Base\n\n")
	builder.WriteString("ฐานความรู้นี้ compile จากข้อมูล aggregate ที่ผ่านกฎความเป็นส่วนตัวแล้ว ไม่เก็บความคิดเห็นดิบ\n\n")
	for _, article := range articles {
		fmt.Fprintf(&builder, "- [[%s]] — %s\n  - %s\n", article.PeriodLabel, article.Entry.TitleTH, article.Entry.SummaryText)
	}
	return builder.String(), nil
}

func round(value float64, places int) float64 {
	scale := 1.0
	for i := 0; i < places; i++ {
		scale *= 10
	}
	if value >= 0 {
		return float64(int(value*scale+0.5)) / scale
	}
	return float64(int(value*scale-0.5)) / scale
}
