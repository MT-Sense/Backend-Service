package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/emergingtopics"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
	"github.com/mt-sense/backend-service/internal/surveyimport"
)

type importRow struct {
	surveyimport.Row
	DepartmentID string
	Redacted     string
}

type importDepartmentCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type importPreview struct {
	RowCount        int                     `json:"rowCount"`
	Departments     []importDepartmentCount `json:"departments"`
	AlreadyImported bool                    `json:"alreadyImported"`
}

type importResult struct {
	Imported int `json:"imported"`
}

type importValidationError struct{ issues []string }

func (e *importValidationError) Error() string { return strings.Join(e.issues, "; ") }

func sendImportError(c *fiber.Ctx, err error) error {
	var validation *importValidationError
	if errors.As(err, &validation) {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: validation.issues})
	}
	return err
}

func (h *PeriodsHandler) prepareImport(c *fiber.Ctx) (*models.SurveyPeriod, []importRow, importPreview, string, error) {
	orgID := middleware.OrgID(c)
	var period models.SurveyPeriod
	if err := h.db.Where("id = ? AND org_id = ?", c.Params("id"), orgID).First(&period).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, importPreview{}, "", fiber.NewError(fiber.StatusNotFound, "survey period not found")
		}
		return nil, nil, importPreview{}, "", err
	}
	data := c.Body()
	rows, issues, err := surveyimport.Parse(data)
	if err != nil {
		return nil, nil, importPreview{}, "", fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	var departments []models.Department
	if err := h.db.Where("org_id = ?", orgID).Find(&departments).Error; err != nil {
		return nil, nil, importPreview{}, "", err
	}
	byName := make(map[string]models.Department, len(departments))
	for _, department := range departments {
		byName[normalizeDepartmentName(department.Name)] = department
	}
	prepared := make([]importRow, 0, len(rows))
	counts := make(map[string]int)
	for _, row := range rows {
		department, found := byName[normalizeDepartmentName(row.Department)]
		if row.Department != "" && !found {
			issues = append(issues, fmt.Sprintf("row %d: department %q does not exist in this organization", row.Number, row.Department))
		}
		redacted := strings.TrimSpace(privacy.Redact(row.Comment))
		if row.Comment != "" && (redacted == "" || redacted == "[ถูกปกปิด]") {
			issues = append(issues, fmt.Sprintf("row %d: comment contains no analyzable text after redaction", row.Number))
		}
		if found {
			counts[department.Name]++
		}
		prepared = append(prepared, importRow{Row: row, DepartmentID: department.ID, Redacted: redacted})
	}
	if len(issues) > 0 {
		if len(issues) > 30 {
			issues = append(issues[:30], fmt.Sprintf("and %d more errors", len(issues)-30))
		}
		return nil, nil, importPreview{}, "", &importValidationError{issues: issues}
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	preview := importPreview{RowCount: len(prepared), Departments: make([]importDepartmentCount, 0, len(names))}
	for _, name := range names {
		preview.Departments = append(preview.Departments, importDepartmentCount{Name: name, Count: counts[name]})
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	var existing int64
	if err := h.db.Model(&models.SurveyImport{}).
		Where("org_id = ? AND period_id = ? AND file_hash = ?", orgID, period.ID, hash).
		Count(&existing).Error; err != nil {
		return nil, nil, importPreview{}, "", err
	}
	preview.AlreadyImported = existing > 0
	return &period, prepared, preview, hash, nil
}

func normalizeDepartmentName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// PreviewImport validates every row and department without contacting AI or writing data.
func (h *PeriodsHandler) PreviewImport(c *fiber.Ctx) error {
	_, _, preview, _, err := h.prepareImport(c)
	if err != nil {
		return sendImportError(c, err)
	}
	return c.JSON(preview)
}

// ImportWorkbook analyzes every redacted comment before one atomic database transaction.
func (h *PeriodsHandler) ImportWorkbook(c *fiber.Ctx) error {
	period, rows, preview, hash, err := h.prepareImport(c)
	if err != nil {
		return sendImportError(c, err)
	}
	if preview.AlreadyImported {
		return fiber.NewError(fiber.StatusConflict, "this workbook was already imported into this period")
	}
	results := make([]aiservice.Result, 0, len(rows))
	for start := 0; start < len(rows); start += 12 {
		end := min(start+12, len(rows))
		texts := make([]string, 0, end-start)
		for _, row := range rows[start:end] {
			texts = append(texts, row.Redacted)
		}
		batch, err := h.ai.AnalyzeMany(c.UserContext(), texts)
		if err != nil {
			log.Printf("survey import analysis failed: %v", err)
			return fiber.NewError(fiber.StatusServiceUnavailable, "AI analysis failed; no rows were imported")
		}
		results = append(results, batch...)
	}
	orgID := middleware.OrgID(c)
	now := time.Now()
	responses := make([]models.SurveyResponse, 0, len(rows))
	analyses := make([]models.ResponseAnalysis, 0, len(rows))
	for i, row := range rows {
		responseID := uuid.NewString()
		departmentID := row.DepartmentID
		responses = append(responses, models.SurveyResponse{
			ID: responseID, OrgID: orgID, PeriodID: period.ID, DepartmentID: &departmentID,
			SatisfactionScore: row.Score, CommentText: row.Redacted, AnalysisStatus: "analyzed", SubmittedAt: now,
		})
		analyses = append(analyses, models.ResponseAnalysis{
			ID: uuid.NewString(), ResponseID: responseID,
			SentimentLabel: results[i].SentimentLabel, SentimentScore: results[i].SentimentScore,
			Confidence: results[i].Confidence, LowConfidence: results[i].LowConfidence,
			Categories: results[i].Categories, Reason: privacy.Redact(results[i].Reason), AnalyzedAt: now,
		})
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&models.SurveyImport{
			ID: uuid.NewString(), OrgID: orgID, PeriodID: period.ID,
			FileHash: hash, RowCount: len(rows), CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.CreateInBatches(&responses, 100).Error; err != nil {
			return err
		}
		if err := tx.CreateInBatches(&analyses, 100).Error; err != nil {
			return err
		}
		for index, response := range responses {
			if err := emergingtopics.Save(tx, orgID, response.ID, results[index].EmergingTopics); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			return fiber.NewError(fiber.StatusConflict, "this workbook was already imported into this period")
		}
		return err
	}
	// Closed periods may already have alert rows; refresh them after their data changes.
	if !period.ClosesAt.After(now) {
		if err := h.stats.WithOrg(orgID).GenerateAlerts(period); err != nil {
			log.Printf("survey import alert refresh failed for period %s: %v", period.ID, err)
		}
	}
	return c.JSON(importResult{Imported: len(rows)})
}
