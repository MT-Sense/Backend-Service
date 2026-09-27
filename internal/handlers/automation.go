package handlers

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/automation"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AutomationHandler struct {
	db                       *gorm.DB
	key, aiURL, serviceToken string
}

func NewAutomationHandler(db *gorm.DB, key, aiURL, token string) *AutomationHandler {
	return &AutomationHandler{db, key, aiURL, token}
}
func (h *AutomationHandler) settings(org string) (models.AutomationSettings, error) {
	s := models.AutomationSettings{OrgID: org, MinResponses: 5, MentionPercent: 30, ConnectorType: "manual", DepartmentProjects: map[string]string{}}
	err := h.db.Where("org_id = ?", org).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s, nil
	}
	if err == nil && s.ConnectorType == "" {
		if s.JiraSite != "" {
			s.ConnectorType = "jira_cloud"
			if s.ToolName == "" {
				s.ToolName = "Jira Cloud"
			}
		} else {
			s.ConnectorType = "manual"
		}
	}
	return s, err
}
func (h *AutomationHandler) GetSettings(c *fiber.Ctx) error {
	s, err := h.settings(middleware.OrgID(c))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"settings": s, "tokenSet": s.JiraToken != "", "encryptionReady": h.key != "", "aiReady": h.serviceToken != ""})
}
func (h *AutomationHandler) SaveSettings(c *fiber.Ctx) error {
	var req struct {
		models.AutomationSettings
		Token      string `json:"token"`
		ClearToken bool   `json:"clearToken"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(400, "invalid settings")
	}
	s := req.AutomationSettings
	s.OrgID = middleware.OrgID(c)
	s.ToolName = strings.TrimSpace(s.ToolName)
	s.ConnectorType = strings.TrimSpace(s.ConnectorType)
	s.ToolInstructions = strings.TrimSpace(s.ToolInstructions)
	if s.ConnectorType == "" {
		if s.JiraSite != "" {
			s.ConnectorType = "jira_cloud"
		} else {
			s.ConnectorType = "manual"
		}
	}
	if s.ConnectorType != "manual" && s.ConnectorType != "jira_cloud" {
		return fiber.NewError(400, "connector type must be manual or jira_cloud")
	}
	if len([]rune(s.ToolName)) > 100 || len([]rune(s.ToolInstructions)) > 2000 {
		return fiber.NewError(400, "tool name or instructions are too long")
	}
	s.JiraSite = strings.TrimRight(strings.TrimSpace(s.JiraSite), "/")
	s.JiraProject = strings.ToUpper(strings.TrimSpace(s.JiraProject))
	s.JiraEmail = strings.TrimSpace(s.JiraEmail)
	if s.MinResponses < 5 || s.MinResponses > 10000 || s.MentionPercent < 1 || s.MentionPercent > 100 {
		return fiber.NewError(400, "minimum responses must be 5–10000 and mention percentage 1–100")
	}
	if len(s.TrainingCatalog) > 20000 || len(s.ApprovalPolicy) > 20000 || len(s.BenefitsPolicy) > 20000 || len(req.Token) > 4096 {
		return fiber.NewError(400, "settings text is too long")
	}
	if s.ConnectorType == "jira_cloud" && s.JiraSite != "" && !automation.ValidSite(s.JiraSite) {
		return fiber.NewError(400, "use https://your-company.atlassian.net without a path")
	}
	if s.ConnectorType == "jira_cloud" && s.JiraProject != "" && !automation.ProjectPattern.MatchString(s.JiraProject) {
		return fiber.NewError(400, "invalid Jira project key")
	}
	if s.ConnectorType == "jira_cloud" && s.JiraIssueType != "" && !automation.IssueTypePattern.MatchString(s.JiraIssueType) {
		return fiber.NewError(400, "Jira issue type must be a numeric ID")
	}
	if s.ConnectorType == "jira_cloud" && s.JiraEmail != "" {
		if _, err := mail.ParseAddress(s.JiraEmail); err != nil {
			return fiber.NewError(400, "invalid Jira email")
		}
	}
	for id, p := range s.DepartmentProjects {
		if s.ConnectorType != "jira_cloud" {
			break
		}
		if p == "" {
			delete(s.DepartmentProjects, id)
			continue
		}
		if !automation.ProjectPattern.MatchString(p) {
			return fiber.NewError(400, "invalid department project key")
		}
		var n int64
		if err := h.db.Model(&models.Department{}).Where("org_id = ? AND id = ?", s.OrgID, id).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return fiber.NewError(400, "unknown department")
		}
	}
	old, err := h.settings(s.OrgID)
	if err != nil {
		return err
	}
	s.JiraToken = old.JiraToken
	if req.ClearToken {
		s.JiraToken = ""
	}
	if req.Token != "" {
		s.JiraToken, err = automation.Encrypt(h.key, s.OrgID, req.Token)
		if err != nil {
			return fiber.NewError(503, "configure a valid AUTOMATION_ENCRYPTION_KEY before saving credentials")
		}
	}
	if old.JiraToken != "" && req.Token == "" && !req.ClearToken && (old.JiraSite != s.JiraSite || old.JiraEmail != s.JiraEmail) {
		return fiber.NewError(400, "enter a new token when changing Jira site or email")
	}
	if s.ConnectorType != "jira_cloud" {
		s.AllowJiraWrite = false
	}
	if s.AllowJiraWrite && (s.JiraSite == "" || s.JiraEmail == "" || s.JiraToken == "" || s.JiraIssueType == "") {
		return fiber.NewError(400, "Jira site, email, token and issue type ID are required for writes")
	}
	// Serialize edits with a row lock; revision changes invalidate reviewed destinations.
	err = h.db.Transaction(func(tx *gorm.DB) error {
		var org models.Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&org, "id = ?", s.OrgID).Error; err != nil {
			return err
		}
		var current models.AutomationSettings
		e := tx.First(&current, "org_id = ?", s.OrgID).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if current.Revision != req.Revision {
			return fiber.NewError(409, "settings changed; reload before saving")
		}
		s.Revision = current.Revision + 1
		return tx.Save(&s).Error
	})
	if err != nil {
		return err
	}
	return h.GetSettings(c)
}
func (h *AutomationHandler) jira(s models.AutomationSettings) (*automation.Jira, error) {
	if s.ConnectorType != "jira_cloud" {
		return nil, errors.New("selected tool uses manual coordination and has no API adapter")
	}
	if s.JiraSite == "" || s.JiraEmail == "" || s.JiraToken == "" {
		return nil, errors.New("Jira is not connected")
	}
	token, err := automation.Decrypt(h.key, s.OrgID, s.JiraToken)
	if err != nil {
		return nil, errors.New("Jira credential cannot be decrypted; check encryption key")
	}
	return automation.NewJira(s.JiraSite, s.JiraEmail, token), nil
}
func (h *AutomationHandler) TestJira(c *fiber.Ctx) error {
	s, err := h.settings(middleware.OrgID(c))
	if err != nil {
		return err
	}
	j, err := h.jira(s)
	if err != nil {
		return fiber.NewError(400, err.Error())
	}
	if err = j.Test(c.UserContext(), s.JiraProject); err != nil {
		return fiber.NewError(502, err.Error())
	}
	return c.JSON(fiber.Map{"message": "เชื่อมต่อ Jira และอ่าน Project ได้สำเร็จ (ยังไม่ได้ทดสอบการสร้างงาน)"})
}
func (h *AutomationHandler) ListProposals(c *fiber.Ctx) error {
	rows := []models.AutomationProposal{}
	q := h.db.Where("org_id = ? AND demo = false", middleware.OrgID(c))
	if p := c.Query("period"); p != "" {
		q = q.Where("period_id = ?", p)
	}
	if err := q.Order("created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		rows[i].MissingData = uniqueNonEmptyStrings(rows[i].MissingData)
	}
	return c.JSON(rows)
}

// uniqueNonEmptyStrings preserves the first occurrence so system supplied
// limitations and matching AI output can be merged without duplicate UI rows.
func uniqueNonEmptyStrings(groups ...[]string) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
func (h *AutomationHandler) Events(c *fiber.Ctx) error {
	events := []models.AutomationEvent{}
	if err := h.db.Where("org_id = ? AND proposal_id = ?", middleware.OrgID(c), c.Params("id")).Order("created_at").Find(&events).Error; err != nil {
		return err
	}
	return c.JSON(events)
}
func event(tx *gorm.DB, p *models.AutomationProposal, actor, kind string) error {
	return tx.Create(&models.AutomationEvent{ID: uuid.NewString(), OrgID: p.OrgID, ProposalID: p.ID, ActorID: actor, Event: kind, Version: p.Version, Snapshot: *p, CreatedAt: time.Now()}).Error
}

// Review locks the proposal and checks the version from the UI. Approval snapshots the
// exact title, body and destination; edits to approved records are prohibited.
func (h *AutomationHandler) Review(c *fiber.Ctx) error {
	var req struct {
		Version  int    `json:"version"`
		Decision string `json:"decision"`
		Title    string `json:"title"`
		Draft    string `json:"draft"`
		Action   string `json:"action"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(400, "invalid review")
	}
	org := middleware.OrgID(c)
	var p models.AutomationProposal
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND id = ?", org, c.Params("id")).First(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fiber.NewError(404, "proposal not found")
			}
			return err
		}
		if req.Decision == "withdraw" {
			if p.Status != "approved" || p.Version != req.Version {
				return fiber.NewError(409, "only the current unexecuted approval can be withdrawn")
			}
			p.Status = "pending"
			p.ReviewedAt = nil
			p.ReviewedBy = ""
			p.Version++
			if err := tx.Save(&p).Error; err != nil {
				return err
			}
			return event(tx, &p, middleware.UserID(c), "approval_withdrawn")
		}
		if p.Status != "pending" || p.Version != req.Version {
			return fiber.NewError(409, "proposal changed or already reviewed; reload")
		}
		switch req.Decision {
		case "edit":
			if strings.TrimSpace(req.Title) == "" || len([]rune(req.Title)) > 200 || strings.TrimSpace(req.Draft) == "" || len(req.Draft) > 12000 {
				return fiber.NewError(400, "title and draft are required (max 200 characters / 12000 bytes)")
			}
			if req.Action != "local_task" && req.Action != "jira_task" {
				return fiber.NewError(400, "unsupported action")
			}
			p.Title = strings.TrimSpace(req.Title)
			p.Draft = strings.TrimSpace(req.Draft)
			p.Action = req.Action
			if p.Action == "jira_task" {
				s, e := h.settings(org)
				if e != nil {
					return e
				}
				if !s.AllowJiraWrite || s.JiraToken == "" || p.Demo {
					return fiber.NewError(400, "enable Jira writes in Settings first; demo cannot write to Jira")
				}
				project := s.JiraProject
				if p.DepartmentID != "" {
					project = s.DepartmentProjects[p.DepartmentID]
				}
				if project == "" {
					return fiber.NewError(400, "configure a Jira project for this scope")
				}
				p.TargetSite = s.JiraSite
				p.TargetProject = project
				p.TargetIssueType = s.JiraIssueType
				p.SettingsRevision = s.Revision
			} else {
				p.TargetSite = ""
				p.TargetProject = ""
				p.TargetIssueType = ""
				p.SettingsRevision = 0
			}
		case "approve", "reject":
			if req.Decision == "approve" {
				if p.Action == "jira_task" {
					s, e := h.settings(org)
					if e != nil {
						return e
					}
					if !s.AllowJiraWrite || s.Revision != p.SettingsRevision {
						return fiber.NewError(409, "Jira settings changed; save the draft with the current destination before approving")
					}
				}
				p.Status = "approved"
			} else {
				p.Status = "rejected"
			}
			now := time.Now()
			p.ReviewedAt = &now
			p.ReviewedBy = middleware.UserID(c)
		default:
			return fiber.NewError(400, "decision must be edit, approve or reject")
		}
		p.Version++
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		return event(tx, &p, middleware.UserID(c), req.Decision)
	})
	if err != nil {
		return err
	}
	return c.JSON(p)
}

// Execution claims a reviewed proposal exactly once before any external write. A timeout
// is ambiguous, so it is never automatically retried. The marker allows manual reconciliation.
func (h *AutomationHandler) Execute(c *fiber.Ctx) error {
	var req struct {
		Version int `json:"version"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(400, "invalid version")
	}
	org := middleware.OrgID(c)
	var p models.AutomationProposal
	var jira *automation.Jira
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND id = ?", org, c.Params("id")).First(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fiber.NewError(404, "proposal not found")
			}
			return err
		}
		if p.Status != "approved" || p.Version != req.Version {
			return fiber.NewError(409, "only the current approved proposal can be executed once")
		}
		if p.Action == "jira_task" {
			if p.Demo {
				return fiber.NewError(400, "demo cannot write to Jira")
			}
			s, e := h.settings(org)
			if e != nil {
				return e
			}
			if !s.AllowJiraWrite || s.Revision != p.SettingsRevision || s.JiraSite != p.TargetSite {
				return fiber.NewError(409, "Jira settings changed; execution blocked")
			}
			jira, e = h.jira(s)
			if e != nil {
				return fiber.NewError(400, e.Error())
			}
		}
		p.Status = "executing"
		p.Version++
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		return event(tx, &p, middleware.UserID(c), "execution_started")
	})
	if err != nil {
		return err
	}
	if p.Action == "local_task" {
		// The approved proposal itself is the internal HR task. Completion is recorded by HR.
		p.Status = "in_progress"
		p.ResultNote = "บันทึกเป็นงานติดตามภายใน HR แล้ว"
		if p.Demo {
			p.ResultNote = "งานติดตามตัวอย่าง — ไม่มีการส่งข้อมูลไปภายนอก"
		}
	} else {
		link, e := jira.Create(c.UserContext(), p.TargetProject, p.TargetIssueType, p.Title, p.Draft, "mtsense-"+p.ID)
		if e != nil {
			p.Status = "needs_check"
			p.ResultNote = fmt.Sprintf("%s. ตรวจ Jira ด้วย label mtsense-%s ก่อนดำเนินการต่อ ระบบจะไม่ส่งซ้ำอัตโนมัติ", e.Error(), p.ID)
		} else {
			p.Status = "executed"
			p.ResultURL = link
			p.ResultNote = "สร้างงานใน Jira สำเร็จ"
		}
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		p.Version++
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		return event(tx, &p, middleware.UserID(c), p.Status)
	})
	if err != nil {
		return err
	}
	return c.JSON(p)
}
func (h *AutomationHandler) Complete(c *fiber.Ctx) error {
	var req struct {
		Version int    `json:"version"`
		Note    string `json:"note"`
	}
	if err := c.BodyParser(&req); err != nil || strings.TrimSpace(req.Note) == "" || len(req.Note) > 2000 {
		return fiber.NewError(400, "completion note is required (max 2000 bytes)")
	}
	var p models.AutomationProposal
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND id = ?", middleware.OrgID(c), c.Params("id")).First(&p).Error; err != nil {
			return fiber.NewError(404, "proposal not found")
		}
		if p.Status != "in_progress" || p.Version != req.Version {
			return fiber.NewError(409, "only an internal task in progress can be completed")
		}
		p.Status = "completed"
		p.ResultNote = strings.TrimSpace(req.Note)
		p.Version++
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		return event(tx, &p, middleware.UserID(c), "completed")
	})
	if err != nil {
		return err
	}
	return c.JSON(p)
}
