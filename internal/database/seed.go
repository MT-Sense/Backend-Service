package database

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/models"
)

// Seed fills an empty database with the same shape of data the frontend was built against.
//
// Responses are generated as real rows rather than precomputed aggregates, so every
// dashboard number is produced by the actual SQL — including the n<5 rule, which the
// deliberately tiny "innovation" team exercises end to end.
func Seed(db *gorm.DB) error {
	// Scoped to the demo org specifically (not "any org exists") so this stays self-healing
	// once real customer orgs exist via self-service signup — every boot re-checks whether
	// the demo org itself is present, rather than treating any org's existence as "already
	// seeded".
	var existing int64
	if err := db.Model(&models.Organization{}).Where("slug = ?", "mt-sense").Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		log.Println("database: demo org already present, skipping seed")
		return nil
	}

	log.Println("database: seeding…")

	if err := db.Transaction(func(tx *gorm.DB) error {
		orgID, err := seedOrganization(tx)
		if err != nil {
			return err
		}
		if err := seedReference(tx, orgID); err != nil {
			return err
		}
		if err := seedUsers(tx, orgID); err != nil {
			return err
		}
		periods, err := seedPeriods(tx, orgID)
		if err != nil {
			return err
		}
		if err := seedResponses(tx, orgID, periods); err != nil {
			return err
		}
		return seedAnalysisOutput(tx, orgID, periods[len(periods)-1])
	}); err != nil {
		return fmt.Errorf("seeding: %w", err)
	}

	log.Println("database: seed complete")
	return nil
}

const seedOrgID = "org-mt-sense"

func seedOrganization(tx *gorm.DB) (string, error) {
	// JoinCode is a fixed, memorable literal (not randomly generated via auth.JoinCode) since
	// the demo org is a known fixture referenced in docs and manual testing.
	org := models.Organization{
		ID: seedOrgID, Name: "MT-Sense Demo Co., Ltd.", Slug: "mt-sense",
		JoinCode: "DEMO01", CollectDepartment: true, CollectTenure: false,
	}
	if err := tx.Create(&org).Error; err != nil {
		return "", err
	}
	return org.ID, nil
}

// departmentSeed mirrors Frontend/src/mocks/departments.ts, including "innovation" sitting
// under the n<5 threshold on purpose.
var departmentSeed = []struct {
	ID          string
	Name        string
	Respondents int
}{
	{"sales", "ฝ่ายขาย", 42},
	{"support", "ฝ่ายซัพพอร์ต", 58},
	{"engineering", "ฝ่ายวิศวกรรม", 76},
	{"product", "ฝ่ายผลิตภัณฑ์", 31},
	{"hr", "ฝ่ายทรัพยากรบุคคล", 12},
	{"finance", "ฝ่ายการเงิน", 19},
	{"marketing", "ฝ่ายการตลาด", 24},
	{"innovation", "ทีมนวัตกรรม", 3}, // < 5 → must stay suppressed
}

// positionSeed is org-wide (positions are not department-scoped in the given schema).
var positionSeed = []struct {
	ID   string
	Name string
}{
	{"staff", "พนักงานทั่วไป"},
	{"lead", "หัวหน้าทีม"},
	{"manager", "ผู้จัดการ"},
	{"specialist", "ผู้เชี่ยวชาญ"},
	{"intern", "พนักงานฝึกงาน"},
}

// topicSeed is the single source of truth for the six dimensions, matching
// Frontend/src/mocks/topics.ts — used to tag categories on response_analysis.
var topicSeed = []struct {
	ID     string
	TH, EN string
}{
	{"work", "งาน", "Work"},
	{"team", "ทีม", "Team"},
	{"manager", "หัวหน้า", "Manager"},
	{"compensation", "ค่าตอบแทน", "Compensation"},
	{"growth", "เติบโต", "Growth"},
	{"benefits", "สวัสดิการ", "Benefits"},
}

func seedReference(tx *gorm.DB, orgID string) error {
	for _, d := range departmentSeed {
		code, err := auth.DepartmentCode()
		if err != nil {
			return err
		}
		dept := models.Department{ID: d.ID, OrgID: orgID, Name: d.Name, JoinCode: &code}
		if err := tx.Create(&dept).Error; err != nil {
			return err
		}
	}
	for _, p := range positionSeed {
		position := models.Position{ID: p.ID, OrgID: orgID, Name: p.Name}
		if err := tx.Create(&position).Error; err != nil {
			return err
		}
	}
	for i, t := range topicSeed {
		topic := models.Topic{ID: t.ID, Label: models.Localized{TH: t.TH, EN: t.EN}, SortOrder: i}
		if err := tx.Create(&topic).Error; err != nil {
			return err
		}
	}
	return nil
}

func seedUsers(tx *gorm.DB, orgID string) error {
	// Demo credentials; the README documents them and they exist only for local work.
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	deptID := func(id string) *string { return &id }
	posID := func(id string) *string { return &id }

	named := []models.User{
		{ID: "u-hr", OrgID: orgID, Email: "hr@mtsense.local", FullName: "พิมพ์ชนก ศรีสุข",
			Role: models.RoleAdmin, DepartmentID: deptID("hr"), PositionID: posID("manager"),
			IsActive: true, NotifyNewRound: true, NotifyMonthlySummary: true},
		{ID: "u-exec", OrgID: orgID, Email: "exec@mtsense.local", FullName: "ธนกร วิริยะกุล",
			Role: models.RoleExecutive, DepartmentID: deptID("hr"), PositionID: posID("manager"),
			IsActive: true, NotifyNewRound: false, NotifyMonthlySummary: true},
		{ID: "u-emp", OrgID: orgID, Email: "employee@mtsense.local", FullName: "อรวรรณ ใจดี",
			Role: models.RoleEmployee, DepartmentID: deptID("engineering"), PositionID: posID("staff"),
			IsActive: true, NotifyNewRound: true, NotifyMonthlySummary: false},
	}
	for i := range named {
		named[i].PasswordHash = string(hash)
		named[i].LastLoginAt = time.Now().Add(-24 * time.Hour)
		if err := tx.Create(&named[i]).Error; err != nil {
			return err
		}
	}

	// Filler headcount so the response-rate denominator is realistic.
	for _, d := range departmentSeed {
		for i := range d.Respondents {
			position := positionSeed[i%len(positionSeed)]
			user := models.User{
				ID:           fmt.Sprintf("u-%s-%d", d.ID, i),
				OrgID:        orgID,
				Email:        fmt.Sprintf("%s.%d@mtsense.local", d.ID, i),
				PasswordHash: string(hash),
				FullName:     fmt.Sprintf("พนักงาน %s %d", d.ID, i+1),
				Role:         models.RoleEmployee,
				DepartmentID: deptID(d.ID),
				PositionID:   posID(position.ID),
				IsActive:     true,
				LastLoginAt:  time.Now().Add(-48 * time.Hour),
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// seedPeriods creates 5 closed monthly rounds plus one currently-open round.
func seedPeriods(tx *gorm.DB, orgID string) ([]models.SurveyPeriod, error) {
	now := time.Now()
	periods := make([]models.SurveyPeriod, 0, 6)

	for monthsAgo := 5; monthsAgo >= 1; monthsAgo-- {
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -monthsAgo, 0)
		monthEnd := monthStart.AddDate(0, 1, 0)
		period := models.SurveyPeriod{
			ID:       uuid.NewString(),
			OrgID:    orgID,
			Month:    int16(monthStart.Month()),
			Year:     int16(monthStart.Year()),
			OpensAt:  monthStart,
			ClosesAt: monthEnd,
		}
		if err := tx.Create(&period).Error; err != nil {
			return nil, err
		}
		periods = append(periods, period)
	}

	// Current period: still open, closes 10 days from now.
	current := models.SurveyPeriod{
		ID:       uuid.NewString(),
		OrgID:    orgID,
		Month:    int16(now.Month()),
		Year:     int16(now.Year()),
		OpensAt:  now.AddDate(0, 0, -5),
		ClosesAt: now.AddDate(0, 0, 10),
	}
	if err := tx.Create(&current).Error; err != nil {
		return nil, err
	}
	periods = append(periods, current)

	return periods, nil
}

// departmentProfile is the mean score per topic used to generate plausible answers, taken
// from the frontend's heatmap mock so the seeded dashboards look familiar. The overall
// satisfaction_score baseline is the mean of these six.
var departmentProfile = map[string][6]float64{
	"sales":       {3.8, 4.1, 3.6, 3.2, 3.4, 3.9},
	"support":     {2.6, 3.3, 3.1, 2.9, 2.7, 3.5},
	"engineering": {3.9, 4.2, 4.0, 3.8, 3.6, 4.0},
	"product":     {3.7, 3.9, 3.5, 3.3, 3.8, 3.7},
	"hr":          {4.0, 4.3, 4.1, 3.6, 3.5, 4.2},
	"finance":     {3.6, 3.8, 3.7, 3.4, 3.2, 3.8},
	"marketing":   {3.5, 3.7, 3.4, 3.1, 3.6, 3.6},
	"innovation":  {4.4, 4.5, 4.2, 3.9, 4.6, 4.0},
}

func averageProfile(profile [6]float64) float64 {
	sum := 0.0
	for _, v := range profile {
		sum += v
	}
	return sum / float64(len(profile))
}

var openTextSamples = []string{
	"อยากให้มีการกระจายงานกะดึกให้เท่ากันมากขึ้น ตอนนี้บางคนหนักกว่าคนอื่นมาก",
	"สวัสดิการด้านสุขภาพจิตดีขึ้นมากในปีนี้ ขอบคุณที่รับฟัง",
	"อยากเห็นเส้นทางเติบโตที่ชัดเจนกว่านี้ ตอนนี้ไม่รู้ว่าต้องทำอะไรถึงจะโตในสายงาน",
	"ทีมดีมากค่ะ บรรยากาศการทำงานสนุก ช่วยเหลือกันดี",
	"ภาระงานหนักเกินไปในช่วงปิดไตรมาส อยากให้วางแผนกำลังคนล่วงหน้า",
	"การสื่อสารนโยบายยังไม่ชัดเจน บางครั้งรู้ข่าวช้ากว่าทีมอื่น",
}

var topicIDOrder = []string{"work", "team", "manager", "compensation", "growth", "benefits"}

// seedResponses writes one response per respondent per period. It generates real rows
// (not precomputed aggregates), including the analysis output, so every dashboard figure is
// produced by live SQL — same design as before, just against the fixed 2-field schema.
func seedResponses(tx *gorm.DB, orgID string, periods []models.SurveyPeriod) error {
	rng := rand.New(rand.NewSource(20260814))

	for pi, period := range periods {
		// A gentle upward drift so the six-month trend line has a direction.
		drift := float64(pi) * 0.06

		for _, d := range departmentSeed {
			responding := max(1, d.Respondents*3/4)
			profile := departmentProfile[d.ID]
			baseline := averageProfile(profile)

			for i := range responding {
				deptID := d.ID
				posID := positionSeed[i%len(positionSeed)].ID

				score := clamp(baseline+drift+rng.NormFloat64()*0.6, 1, 5)
				response := models.SurveyResponse{
					ID:                uuid.NewString(),
					OrgID:             orgID,
					PeriodID:          period.ID,
					DepartmentID:      &deptID,
					PositionID:        &posID,
					SatisfactionScore: int16(score + 0.5),
					AnalysisStatus:    "skipped",
					SubmittedAt:       period.OpensAt.AddDate(0, 0, rng.Intn(20)),
				}

				// One in five leaves a comment, which drives an analysis row.
				var analysis *models.ResponseAnalysis
				if rng.Intn(5) == 0 {
					text := openTextSamples[rng.Intn(len(openTextSamples))]
					response.CommentText = text
					response.AnalysisStatus = "analyzed"

					// Weight category selection toward the department's weaker topics so
					// the heatmap/word-cloud/topic-drilldown seed data looks plausible.
					weakest := weakestTopics(profile, 2)
					sentimentLabel, sentimentScore := seedSentiment(rng)

					analysis = &models.ResponseAnalysis{
						ID:             uuid.NewString(),
						ResponseID:     response.ID,
						SentimentLabel: sentimentLabel,
						SentimentScore: sentimentScore,
						Confidence:     float32(0.6 + rng.Float64()*0.3),
						LowConfidence:  rng.Intn(10) == 0,
						Categories:     weakest,
						AnalyzedAt:     response.SubmittedAt,
					}
				}

				if err := tx.Create(&response).Error; err != nil {
					return err
				}
				if analysis != nil {
					if err := tx.Create(analysis).Error; err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// weakestTopics returns the n topic ids with the lowest profile scores.
func weakestTopics(profile [6]float64, n int) []string {
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, len(topicIDOrder))
	for i, id := range topicIDOrder {
		ranked[i] = scored{id: id, score: profile[i]}
	}
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score < ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	if n > len(ranked) {
		n = len(ranked)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ranked[i].id)
	}
	return out
}

func seedSentiment(rng *rand.Rand) (label string, score float32) {
	switch n := rng.Intn(100); {
	case n < 58:
		return "pos", float32(0.3 + rng.Float64()*0.5)
	case n < 87:
		return "neu", 0
	default:
		return "neg", float32(-(0.3 + rng.Float64()*0.5))
	}
}

func seedAnalysisOutput(tx *gorm.DB, orgID string, currentPeriod models.SurveyPeriod) error {
	insight := models.AIInsight{
		OrgID:        orgID,
		PeriodID:     currentPeriod.ID,
		OverallScore: 78,
		Confidence:   "high",
		Summary: models.Localized{
			TH: "ความพอใจโดยรวมทรงตัวและมีแนวโน้มดีขึ้นด้านการทำงานเป็นทีม แต่ภาระงานและค่าตอบแทนยังเป็นประเด็นหลักที่พนักงานพูดถึง",
			EN: "Overall satisfaction is steady with teamwork improving, but workload and compensation remain the top recurring themes.",
		},
		IssueConfidence: []models.IssueConfidence{
			{Label: models.Localized{TH: "ภาระงาน", EN: "Workload"}, Confidence: 44},
			{Label: models.Localized{TH: "ค่าตอบแทน", EN: "Compensation"}, Confidence: 33},
			{Label: models.Localized{TH: "เติบโต", EN: "Growth"}, Confidence: 25},
		},
	}
	if err := tx.Create(&insight).Error; err != nil {
		return err
	}

	urgent := []models.UrgentIssue{
		{ID: "urg-1", OrgID: orgID, PeriodID: currentPeriod.ID, Rank: 1, DepartmentID: "support",
			Label:          models.Localized{TH: "ภาระงาน", EN: "Workload"},
			DepartmentName: models.Localized{TH: "ฝ่ายซัพพอร์ต", EN: "Support"}},
		{ID: "urg-2", OrgID: orgID, PeriodID: currentPeriod.ID, Rank: 2, DepartmentID: "marketing",
			Label:          models.Localized{TH: "ค่าตอบแทน", EN: "Compensation"},
			DepartmentName: models.Localized{TH: "ฝ่ายการตลาด", EN: "Marketing"}},
		{ID: "urg-3", OrgID: orgID, PeriodID: currentPeriod.ID, Rank: 3, DepartmentID: "",
			Label:          models.Localized{TH: "การสื่อสารนโยบาย", EN: "Policy communication"},
			DepartmentName: models.Localized{TH: "ทั้งองค์กร", EN: "Company-wide"}},
	}
	if err := tx.Create(&urgent).Error; err != nil {
		return err
	}

	decisions := []models.DecisionItem{
		{ID: "dec-1", OrgID: orgID, PeriodID: currentPeriod.ID, Rank: 1, Severity: 82, Label: models.Localized{TH: "ภาระงานฝ่ายซัพพอร์ต", EN: "Support team workload"}},
		{ID: "dec-2", OrgID: orgID, PeriodID: currentPeriod.ID, Rank: 2, Severity: 58, Label: models.Localized{TH: "เส้นทางเติบโต", EN: "Career growth paths"}},
		{ID: "dec-3", OrgID: orgID, PeriodID: currentPeriod.ID, Rank: 3, Severity: 50, Label: models.Localized{TH: "การสื่อสารนโยบาย", EN: "Policy communication"}},
	}
	if err := tx.Create(&decisions).Error; err != nil {
		return err
	}

	terms := []models.WordCloudTerm{
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 92, TopicID: "work", Term: models.Localized{TH: "ภาระงาน", EN: "Workload"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 71, TopicID: "manager", Term: models.Localized{TH: "หัวหน้าทีม", EN: "Team lead"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 66, TopicID: "benefits", Term: models.Localized{TH: "สวัสดิการ", EN: "Benefits"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 54, TopicID: "growth", Term: models.Localized{TH: "เติบโต", EN: "Growth"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 48, TopicID: "compensation", Term: models.Localized{TH: "ค่าตอบแทน", EN: "Compensation"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 39, TopicID: "work", Term: models.Localized{TH: "กะดึก", EN: "Night shift"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 33, TopicID: "team", Term: models.Localized{TH: "ทีม", EN: "Team"}},
		{OrgID: orgID, PeriodID: currentPeriod.ID, Frequency: 28, TopicID: "growth", Term: models.Localized{TH: "สื่อสาร", EN: "Communication"}},
	}
	if err := tx.Create(&terms).Error; err != nil {
		return err
	}

	subIssues := []models.TopicSubIssue{
		{ID: "sub-work-1", TopicID: "work", Percentage: 44, Label: models.Localized{TH: "งานเข้าไม่สม่ำเสมอ", EN: "Uneven workload"}},
		{ID: "sub-work-2", TopicID: "work", Percentage: 31, Label: models.Localized{TH: "คนไม่พอในกะดึก", EN: "Understaffed night shifts"}},
		{ID: "sub-work-3", TopicID: "work", Percentage: 25, Label: models.Localized{TH: "OT บ่อยเกินไป", EN: "Too much overtime"}},
		{ID: "sub-comp-1", TopicID: "compensation", Percentage: 40, Label: models.Localized{TH: "ปรับเงินเดือนล่าช้า", EN: "Slow salary reviews"}},
		{ID: "sub-comp-2", TopicID: "compensation", Percentage: 33, Label: models.Localized{TH: "เกณฑ์โบนัสไม่ชัดเจน", EN: "Unclear bonus criteria"}},
		{ID: "sub-growth-1", TopicID: "growth", Percentage: 38, Label: models.Localized{TH: "เส้นทางอาชีพไม่ชัดเจน", EN: "Unclear career path"}},
	}
	if err := tx.Create(&subIssues).Error; err != nil {
		return err
	}

	// Sample quotes are stored post-redaction; nothing raw is kept.
	quotes := []models.TopicSampleQuote{
		{TopicID: "work", Text: "ช่วงนี้งานเข้าไม่สม่ำเสมอ บางสัปดาห์แทบไม่มีเวลาพัก"},
		{TopicID: "work", Text: "อยากให้มีการวางแผนกำลังคนล่วงหน้ามากขึ้น"},
		{TopicID: "compensation", Text: "อยากให้มีความชัดเจนเรื่องเกณฑ์โบนัสมากขึ้น"},
		{TopicID: "growth", Text: "ยังไม่รู้ว่าต้องทำอะไรถึงจะเติบโตในสายงานนี้"},
	}
	if err := tx.Create(&quotes).Error; err != nil {
		return err
	}

	summaries := []models.PublishedSummary{
		{ID: "sum-1", OrgID: orgID, PublishedAt: time.Now().AddDate(0, 0, -13).Format("2006-01-02"),
			Title: models.Localized{TH: "สรุปเดือนที่ผ่านมา", EN: "Last month's summary"},
			Body: models.Localized{
				TH: "eNPS ปรับตัวดีขึ้นจากเดือนก่อน ประเด็นหลักที่พนักงานพูดถึงคือภาระงานและเส้นทางเติบโต",
				EN: "eNPS improved from last month. The main themes raised were workload and growth path.",
			}},
	}
	if err := tx.Create(&summaries).Error; err != nil {
		return err
	}

	actions := []models.ActionItem{
		{ID: "act-1", OrgID: orgID, TopicID: "benefits", Assignee: "ฝ่ายทรัพยากรบุคคล", Status: "done", CreatedBy: models.RoleAdmin, Level: "full",
			TargetDate: time.Now().AddDate(0, -1, 0).Format("2006-01-02"),
			Topic:      models.Localized{TH: "เพิ่มวันลาพักใจ 2 วัน/ปี", EN: "Added 2 wellness days per year"}},
		{ID: "act-2", OrgID: orgID, TopicID: "work", Assignee: "หัวหน้าฝ่ายซัพพอร์ต", Status: "in_progress", CreatedBy: models.RoleAdmin, Level: "full",
			TargetDate: time.Now().AddDate(0, 1, 0).Format("2006-01-02"),
			Topic:      models.Localized{TH: "ทบทวนตารางกะดึก", EN: "Reviewing night-shift scheduling"}},
		{ID: "act-3", OrgID: orgID, TopicID: "manager", Assignee: "ฝ่ายทรัพยากรบุคคล", Status: "in_progress", CreatedBy: models.RoleExecutive, Level: "decision",
			TargetDate: time.Now().AddDate(0, 2, 0).Format("2006-01-02"),
			Topic:      models.Localized{TH: "อบรมหัวหน้าทีมเรื่องการให้ฟีดแบ็ก", EN: "Manager feedback training"}},
	}
	if err := tx.Create(&actions).Error; err != nil {
		return err
	}

	// Feed posts covering every gate combination, matching Frontend/src/mocks/feed.ts.
	today := time.Now()
	posts := []models.FeedPost{
		{ID: "f1", OrgID: orgID, Text: "อยากให้มีการกระจายงานกะดึกให้เท่ากันมากขึ้น ตอนนี้บางคนหนักกว่าคนอื่นมาก",
			Hashtags: []string{"ภาระงาน", "กะดึก"}, PostedOn: today.AddDate(0, 0, -9).Format("2006-01-02"),
			Upvotes: 142, Downvotes: 4, HRReplied: true, OptedIn: true, Published: true},
		{ID: "f2", OrgID: orgID, Text: "สวัสดิการด้านสุขภาพจิตดีขึ้นมากในปีนี้ ขอบคุณที่รับฟัง",
			Hashtags: []string{"สวัสดิการ"}, PostedOn: today.AddDate(0, 0, -10).Format("2006-01-02"),
			Upvotes: 98, Downvotes: 1, OptedIn: true, Published: true},
		{ID: "f3", OrgID: orgID, Text: "อยากเห็นเส้นทางเติบโตที่ชัดเจนกว่านี้ ตอนนี้ไม่รู้ว่าต้องทำอะไรถึงจะโตในสายงาน",
			Hashtags: []string{"เติบโต"}, PostedOn: today.AddDate(0, 0, -11).Format("2006-01-02"),
			Upvotes: 76, Downvotes: 2, HRReplied: true, OptedIn: true, Published: true},
		{ID: "f4", OrgID: orgID, Text: "ทีมดีมากค่ะ บรรยากาศการทำงานสนุก ช่วยเหลือกันดี",
			Hashtags: []string{"ทีม"}, PostedOn: today.AddDate(0, 0, -12).Format("2006-01-02"),
			Upvotes: 54, OptedIn: true, Published: true},
		// Opted in but not yet moderated — must NOT appear in the public feed.
		{ID: "f5", OrgID: orgID, Text: "ข้อความนี้เพิ่งส่งเข้าระบบ อยู่ระหว่างตรวจสอบก่อนเผยแพร่",
			Hashtags: []string{"ภาระงาน"}, PostedOn: today.Format("2006-01-02"),
			OptedIn: true, Published: false},
		// Author never opted in — must NOT appear regardless of moderation.
		{ID: "f6", OrgID: orgID, Text: "ความเห็นนี้ผู้ตอบไม่ได้เลือกเผยแพร่ จึงไม่ปรากฏใน feed",
			Hashtags: []string{"ค่าตอบแทน"}, PostedOn: today.AddDate(0, 0, -1).Format("2006-01-02"),
			OptedIn: false, Published: false},
	}
	return tx.Create(&posts).Error
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
