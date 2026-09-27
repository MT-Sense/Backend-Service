package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validRoutingDepartment(db *gorm.DB, org, id string) error {
	var n int64
	if err := db.Model(&models.Department{}).Where("org_id = ? AND id = ?", org, id).Count(&n).Error; err != nil {
		return err
	}
	if n != 1 {
		return fiber.NewError(400, "กรุณาเลือกแผนกในองค์กรนี้")
	}
	return nil
}
func (h *AutomationHandler) RoutingPolicy(c *fiber.Ctx) error {
	p := models.RoutingPolicy{OrgID: middleware.OrgID(c), Rules: []models.RoutingRule{}}
	err := h.db.First(&p, "org_id = ?", p.OrgID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return c.JSON(p)
}
func (h *AutomationHandler) SaveRoutingPolicy(c *fiber.Ctx) error {
	var p models.RoutingPolicy
	if c.BodyParser(&p) != nil || len(p.Rules) > 50 {
		return fiber.NewError(400, "กำหนดกฎได้ไม่เกิน 50 กฎ")
	}
	p.OrgID = middleware.OrgID(c)
	seen := map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		r.Topic = strings.TrimSpace(r.Topic)
		if r.ID == "" {
			r.ID = uuid.NewString()
		}
		if seen[r.ID] || len(r.ID) > 64 || r.Topic == "" || len([]rune(r.Topic)) > 100 || len(r.Keywords) == 0 || len(r.Keywords) > 20 {
			return fiber.NewError(400, "ตรวจชื่อหัวข้อและคำสำคัญ (1–20 คำต่อกฎ)")
		}
		seen[r.ID] = true
		for j, k := range r.Keywords {
			k = strings.TrimSpace(k)
			if k == "" || len([]rune(k)) > 80 {
				return fiber.NewError(400, "คำสำคัญต้องมี 1–80 ตัวอักษร")
			}
			r.Keywords[j] = k
		}
		if err := validRoutingDepartment(h.db, p.OrgID, r.DepartmentID); err != nil {
			return err
		}
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		var org models.Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&org, "id = ?", p.OrgID).Error; err != nil {
			return err
		}
		var old models.RoutingPolicy
		err := tx.First(&old, "org_id = ?", p.OrgID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if old.Version != p.Version {
			return fiber.NewError(409, "กฎเปลี่ยนแล้ว กรุณาโหลดใหม่")
		}
		p.Version++
		return tx.Save(&p).Error
	})
	if err != nil {
		return err
	}
	return c.JSON(p)
}

// Suggestions never dispatch automatically. Multiple matching rules require human triage.
func matchRouting(rules []models.RoutingRule, text string) (string, string, string) {
	text = strings.ToLower(text)
	matches := []models.RoutingRule{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, k := range r.Keywords {
			if strings.TrimSpace(k) != "" && strings.Contains(text, strings.ToLower(k)) {
				matches = append(matches, r)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0].Topic, matches[0].DepartmentID, "ตรงคำสำคัญในกฎ: " + matches[0].Topic + " — รอ HR ตรวจความหมาย"
	}
	if len(matches) > 1 {
		return "", "", "ตรงหลายกฎ — กรุณาเลือกเจ้าของเรื่อง หรือแยกเป็นหลายเรื่อง"
	}
	return "", "", "ไม่พบกฎ — รอ HR คัดแยกหัวข้อใหม่"
}
func (h *AutomationHandler) ListRoutingCases(c *fiber.Ctx) error {
	rows := []models.RoutingCase{}
	if err := h.db.Where("org_id = ? AND title NOT LIKE ?", middleware.OrgID(c), "[ตัวอย่าง]%").Order("created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	return c.JSON(rows)
}
func (h *AutomationHandler) CreateRoutingCase(c *fiber.Ctx) error {
	var req struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if c.BodyParser(&req) != nil || strings.TrimSpace(req.Title) == "" || len([]rune(req.Title)) > 200 || strings.TrimSpace(req.Summary) == "" || len([]rune(req.Summary)) > 3000 {
		return fiber.NewError(400, "กรอกชื่อเรื่องและสรุป (ไม่เกิน 200 / 3000 ตัวอักษร)")
	}
	p := models.RoutingCase{ID: uuid.NewString(), OrgID: middleware.OrgID(c), Title: strings.TrimSpace(req.Title), Summary: strings.TrimSpace(req.Summary), Status: "triage", Version: 1}
	var policy models.RoutingPolicy
	err := h.db.First(&policy, "org_id = ?", p.OrgID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	p.Topic, p.DepartmentID, p.MatchReason = matchRouting(policy.Rules, p.Title+" "+p.Summary)
	p.History = []models.RoutingHistory{{At: time.Now(), Actor: middleware.UserID(c), Action: "created", Note: p.MatchReason, Topic: p.Topic, DepartmentID: p.DepartmentID}}
	if err := h.db.Create(&p).Error; err != nil {
		return err
	}
	return c.Status(201).JSON(p)
}
func (h *AutomationHandler) UpdateRoutingCase(c *fiber.Ctx) error {
	var req struct {
		Version      int    `json:"version"`
		Action       string `json:"action"`
		Topic        string `json:"topic"`
		DepartmentID string `json:"departmentId"`
		Note         string `json:"note"`
	}
	if c.BodyParser(&req) != nil || len([]rune(req.Note)) > 1000 {
		return fiber.NewError(400, "บันทึกไม่ถูกต้องหรือยาวเกิน 1000 ตัวอักษร")
	}
	req.Topic = strings.TrimSpace(req.Topic)
	req.Note = strings.TrimSpace(req.Note)
	var p models.RoutingCase
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND id = ?", middleware.OrgID(c), c.Params("id")).First(&p).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fiber.NewError(404, "ไม่พบเรื่อง")
			}
			return err
		}
		if p.Version != req.Version {
			return fiber.NewError(409, "เรื่องนี้เปลี่ยนแล้ว กรุณาโหลดใหม่")
		}
		switch req.Action {
		case "route":
			if p.Status != "triage" {
				return fiber.NewError(409, "ต้องอยู่ในสถานะรอคัดแยก")
			}
			if req.Topic == "" || len([]rune(req.Topic)) > 100 {
				return fiber.NewError(400, "ระบุหัวข้อไม่เกิน 100 ตัวอักษร")
			}
			if err := validRoutingDepartment(tx, p.OrgID, req.DepartmentID); err != nil {
				return err
			}
			p.Topic = req.Topic
			p.DepartmentID = req.DepartmentID
			p.Status = "routed"
		case "acknowledge":
			if p.Status != "routed" {
				return fiber.NewError(409, "ยังไม่ได้บันทึกส่งต่อ")
			}
			p.Status = "in_progress"
		case "complete":
			if p.Status != "in_progress" {
				return fiber.NewError(409, "ต้องรับเรื่องก่อนปิดเรื่อง")
			}
			p.Status = "completed"
		case "retriage":
			if p.Status == "completed" || p.Status == "triage" {
				return fiber.NewError(409, "ไม่สามารถคืนคัดแยกในสถานะนี้")
			}
			p.Status = "triage"
			p.DepartmentID = ""
		default:
			return fiber.NewError(400, "ไม่รองรับการดำเนินการนี้")
		}
		if req.Note == "" {
			return fiber.NewError(400, "กรุณาบันทึกเหตุผลหรือผลการประสานงาน")
		}
		p.Version++
		p.History = append(p.History, models.RoutingHistory{At: time.Now(), Actor: middleware.UserID(c), Action: req.Action, Note: req.Note, Topic: p.Topic, DepartmentID: p.DepartmentID})
		return tx.Save(&p).Error
	})
	if err != nil {
		return err
	}
	return c.JSON(p)
}
