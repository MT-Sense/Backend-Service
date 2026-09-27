package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/automation"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

var playbooks = []struct{ ID, Topic, Name string }{
	{"workload", "work", "ทบทวนภาระงาน"},
	{"approvals", "manager", "ทบทวนกระบวนการอนุมัติ"},
	{"training", "growth", "พัฒนาทักษะและการอบรม"},
	{"benefits", "benefits", "ชี้แจงสวัสดิการ"},
}

func scopeKey(org, period, department, playbook string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(org+"/"+period+"/"+department+"/"+playbook)))
}

func (h *AutomationHandler) Generate(c *fiber.Ctx) error {
	var req struct {
		PeriodID     string `json:"periodId"`
		DepartmentID string `json:"departmentId"`
	}
	if err := c.BodyParser(&req); err != nil || req.PeriodID == "" {
		return fiber.NewError(400, "select a survey period")
	}
	org := middleware.OrgID(c)
	var period models.SurveyPeriod
	if err := h.db.Where("org_id = ? AND id = ?", org, req.PeriodID).First(&period).Error; err != nil {
		return fiber.NewError(404, "survey period not found")
	}
	if req.DepartmentID != "" {
		var n int64
		if err := h.db.Model(&models.Department{}).Where("org_id = ? AND id = ?", org, req.DepartmentID).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return fiber.NewError(404, "department not found")
		}
	}
	settings, err := h.settings(org)
	if err != nil {
		return err
	}
	base := func() *gorm.DB {
		q := h.db.Table("survey_responses sr").Joins("JOIN response_analysis ra ON ra.response_id = sr.id").Where("sr.org_id = ? AND sr.period_id = ?", org, period.ID)
		if req.DepartmentID != "" {
			q = q.Where("sr.department_id = ?", req.DepartmentID)
		}
		return q
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return err
	}
	if total < int64(settings.MinResponses) {
		return c.JSON(fiber.Map{"created": 0, "message": "คำตอบที่วิเคราะห์แล้วยังไม่ถึงเกณฑ์ขั้นต่ำของกลุ่มนี้"})
	}
	candidates := []automation.Candidate{}
	notices := []string{}
	project := settings.JiraProject
	if req.DepartmentID != "" {
		project = settings.DepartmentProjects[req.DepartmentID]
	}
	jiraText := ""
	jiraTried := false
	jiraMissing := ""
	if settings.ConnectorType != "jira_cloud" {
		name := settings.ToolName
		if name == "" {
			name = "เครื่องมือที่ HR เลือก"
		}
		jiraMissing = name + " ตั้งเป็นการประสานงานแบบ Manual จึงไม่มีข้อมูล API สำหรับใช้วิเคราะห์"
	}
	for _, book := range playbooks {
		var existing int64
		if err := h.db.Model(&models.AutomationProposal{}).Where("scope_key = ?", scopeKey(org, period.ID, req.DepartmentID, book.ID)).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			continue
		}
		category, _ := json.Marshal([]string{book.Topic})
		var count int64
		if err := base().Where("ra.categories::jsonb @> ?::jsonb", string(category)).Count(&count).Error; err != nil {
			return err
		}
		if count < int64(settings.MinResponses) || float64(count)*100/float64(total) < float64(settings.MentionPercent) {
			continue
		}
		var samples []string
		if err := base().Where("ra.categories::jsonb @> ?::jsonb", string(category)).Order("sr.submitted_at DESC, sr.id").Limit(20).Pluck("sr.comment_text", &samples).Error; err != nil {
			return err
		}
		for i, t := range samples {
			samples[i] = privacy.Redact(t)
			if len([]rune(samples[i])) > 1200 {
				samples[i] = string([]rune(samples[i])[:1200])
			}
		}
		candidate := automation.Candidate{Playbook: book.ID, Samples: samples, MissingData: []string{}, Evidence: []models.AutomationEvidence{{ID: "survey", Source: "survey", Text: fmt.Sprintf("รอบ %04d-%02d: จากคำตอบที่วิเคราะห์แล้ว %d คำตอบ มี %d คำตอบกล่าวถึงหมวด %s (%.1f%%) ตัวอย่างล่าสุดสูงสุด 20 ข้อความ หมวดนี้อาจมีทั้งข้อความเชิงบวกและเชิงลบ และไม่ยืนยันปัญหาเฉพาะเรื่อง", period.Year, period.Month, total, count, book.Topic, float64(count)*100/float64(total))}}}
		knowledge := ""
		switch book.ID {
		case "training":
			knowledge = settings.TrainingCatalog
		case "approvals":
			knowledge = settings.ApprovalPolicy
		case "benefits":
			knowledge = settings.BenefitsPolicy
		}
		if knowledge != "" {
			candidate.Evidence = append(candidate.Evidence, models.AutomationEvidence{ID: "knowledge", Source: "hr_settings", Text: privacy.Redact(knowledge)})
		} else if book.ID != "workload" {
			candidate.MissingData = append(candidate.MissingData, "ยังไม่มีเอกสารอ้างอิงสำหรับหัวข้อนี้ใน Settings; ห้ามแต่งนโยบาย หลักสูตร หรืองบประมาณ")
		}
		if book.ID == "workload" || book.ID == "approvals" {
			if !jiraTried && settings.ConnectorType == "jira_cloud" {
				jiraTried = true
				if project == "" {
					jiraMissing = "ยังไม่ได้กำหนด Jira Project สำหรับกลุ่มนี้"
				} else {
					j, e := h.jira(settings)
					if e != nil {
						jiraMissing = e.Error()
					} else {
						jiraText, e = j.Snapshot(c.UserContext(), project)
						if e != nil {
							jiraMissing = e.Error()
						}
					}
				}
			}
			if jiraText != "" {
				candidate.Evidence = append(candidate.Evidence, models.AutomationEvidence{ID: "jira", Source: "jira", Text: jiraText})
			} else {
				candidate.MissingData = append(candidate.MissingData, jiraMissing)
			}
			if book.ID == "approvals" {
				candidate.MissingData = append(candidate.MissingData, "ยังไม่มีประวัติ transition/changelog จึงยังยืนยันระยะเวลารออนุมัติไม่ได้")
			}
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return c.JSON(fiber.Map{"created": 0, "message": "ยังไม่มีหัวข้อใหม่ผ่านเกณฑ์ หรือมีข้อเสนอสำหรับหัวข้อนั้นในรอบและกลุ่มนี้แล้ว"})
	}
	drafts, err := automation.Plan(c.UserContext(), h.aiURL, h.serviceToken, candidates)
	if err != nil {
		return fiber.NewError(502, err.Error())
	}
	byBook := map[string]automation.Candidate{}
	for _, b := range candidates {
		byBook[b.Playbook] = b
	}
	created := 0
	err = h.db.Transaction(func(tx *gorm.DB) error {
		for _, d := range drafts {
			if !d.ProblemFound {
				notices = append(notices, d.Playbook+": ไม่พบปัญหาเฉพาะเรื่องที่มีหลักฐานเพียงพอ")
				continue
			}
			candidate := byBook[d.Playbook]
			p := models.AutomationProposal{ID: uuid.NewString(), OrgID: org, ScopeKey: scopeKey(org, period.ID, req.DepartmentID, d.Playbook), PeriodID: period.ID, DepartmentID: req.DepartmentID, Playbook: d.Playbook, Status: "pending", Version: 1, Title: privacy.Redact(d.Title), Rationale: privacy.Redact(d.Rationale), Draft: privacy.Redact(d.Draft), MissingData: uniqueNonEmptyStrings(candidate.MissingData, d.MissingData), Evidence: candidate.Evidence, Action: "local_task"}
			res := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope_key"}}, DoNothing: true}).Create(&p)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue
			}
			if err := event(tx, &p, middleware.UserID(c), "generated"); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"created": created, "message": fmt.Sprintf("สร้างข้อเสนอ %d รายการ พร้อมให้ HR ตรวจ", created), "notices": notices})
}

// Explicit demo fixtures require no credentials and cannot execute an external action.
func (h *AutomationHandler) Demo(c *fiber.Ctx) error {
	org := middleware.OrgID(c)
	created := []models.AutomationProposal{}
	bodies := []string{
		"นัดหัวหน้าทีมทบทวน backlog และงานเกินกำหนด ตรวจสอบขนาดงานและข้อจำกัดก่อนตัดสินใจจัดสรรงานใหม่",
		"ทบทวนขั้นตอนอนุมัติร่วมกับเจ้าของกระบวนการ เก็บเวลารอแต่ละขั้นเพื่อประเมินจุดติดขัด แล้วเสนอ SLA และผู้อนุมัติสำรอง",
		"สำรวจทักษะที่ทีมต้องการเพิ่ม และจัดทำร่างแผนอบรม โดยขอข้อมูลหลักสูตร ระยะเวลา และงบประมาณจาก HR ก่อนเลือกหลักสูตร",
		"ตรวจคู่มือสวัสดิการฉบับที่ใช้อยู่ จัดทำร่าง FAQ เรื่องสิทธิ์และวิธีเบิก ให้เจ้าของนโยบายตรวจความถูกต้องก่อนเผยแพร่",
	}
	examples := []string{"พนักงาน 8 จาก 20 คนสะท้อนงานล้น; Jira จำลองมีงานเปิด 18 งาน เกินกำหนด 7 งาน", "พนักงาน 6 จาก 20 คนสะท้อนการรออนุมัติ", "พนักงาน 9 จาก 20 คนขอพัฒนาทักษะระบบใหม่", "พนักงาน 7 จาก 20 คนไม่เข้าใจขั้นตอนเบิกสวัสดิการ"}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		for i, b := range playbooks {
			id := uuid.NewString()
			p := models.AutomationProposal{ID: id, ScopeKey: "demo-" + id, OrgID: org, Playbook: b.ID, Demo: true, Status: "pending", Version: 1, Title: "[ตัวอย่าง] " + b.Name, Rationale: "ข้อมูลจำลองสำหรับทดลอง workflow เท่านั้น: " + examples[i], Draft: bodies[i], Action: "local_task", Evidence: []models.AutomationEvidence{{ID: "demo", Source: "demo", Text: examples[i]}}, MissingData: []string{"ใช้ข้อมูลจำลอง ไม่ใช่ข้อสรุปจากพนักงานหรือ Jira จริง"}, CreatedAt: time.Now()}
			if err := tx.Create(&p).Error; err != nil {
				return err
			}
			if err := event(tx, &p, middleware.UserID(c), "demo_created"); err != nil {
				return err
			}
			created = append(created, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return c.Status(201).JSON(created)
}
