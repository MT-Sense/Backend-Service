package database

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/models"
)

// Seed fills an empty database with the same shape of data the frontend mocks describe.
//
// Responses are generated as real rows rather than precomputed aggregates, so every
// dashboard number is produced by the actual SQL — including the n<5 rule, which the
// deliberately tiny "innovation" team exercises end to end.
func Seed(db *gorm.DB) error {
	var existing int64
	if err := db.Model(&models.User{}).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		log.Println("database: seed skipped, data already present")
		return nil
	}

	log.Println("database: seeding…")

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := seedReference(tx); err != nil {
			return err
		}
		if err := seedUsers(tx); err != nil {
			return err
		}
		if err := seedSurvey(tx); err != nil {
			return err
		}
		if err := seedResponses(tx); err != nil {
			return err
		}
		return seedAnalysisOutput(tx)
	}); err != nil {
		return fmt.Errorf("seeding: %w", err)
	}

	log.Println("database: seed complete")
	return nil
}

// departmentSeed mirrors src/mocks/departments.ts, including "innovation" sitting under
// the n<5 threshold on purpose.
var departmentSeed = []struct {
	ID          string
	TH, EN      string
	Respondents int
}{
	{"sales", "ฝ่ายขาย", "Sales", 42},
	{"support", "ฝ่ายซัพพอร์ต", "Support", 58},
	{"engineering", "ฝ่ายวิศวกรรม", "Engineering", 76},
	{"product", "ฝ่ายผลิตภัณฑ์", "Product", 31},
	{"hr", "ฝ่ายทรัพยากรบุคคล", "HR", 12},
	{"finance", "ฝ่ายการเงิน", "Finance", 19},
	{"marketing", "ฝ่ายการตลาด", "Marketing", 24},
	{"innovation", "ทีมนวัตกรรม", "Innovation Team", 3}, // < 5 → must stay suppressed
}

// topicSeed is the single source of truth for the six dimensions, matching src/mocks/topics.ts.
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

func seedReference(tx *gorm.DB) error {
	for _, d := range departmentSeed {
		dept := models.Department{ID: d.ID, Name: models.Localized{TH: d.TH, EN: d.EN}}
		if err := tx.Create(&dept).Error; err != nil {
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

func seedUsers(tx *gorm.DB) error {
	// Demo credentials; the README documents them and they exist only for local work.
	hash, err := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	named := []models.User{
		{ID: "u-hr", Email: "hr@mtsense.local", FullName: "พิมพ์ชนก ศรีสุข", Role: models.RoleHR, DepartmentID: "hr", TenureBucket: "3-5y", NotifyNewRound: true, NotifyMonthlySummary: true},
		{ID: "u-exec", Email: "exec@mtsense.local", FullName: "ธนกร วิริยะกุล", Role: models.RoleExecutive, DepartmentID: "hr", TenureBucket: "5y+", NotifyNewRound: false, NotifyMonthlySummary: true},
		{ID: "u-emp", Email: "employee@mtsense.local", FullName: "อรวรรณ ใจดี", Role: models.RoleEmployee, DepartmentID: "engineering", TenureBucket: "1-3y", NotifyNewRound: true, NotifyMonthlySummary: false},
	}
	for i := range named {
		named[i].PasswordHash = string(hash)
		named[i].LastLoginAt = time.Now().Add(-24 * time.Hour)
		if err := tx.Create(&named[i]).Error; err != nil {
			return err
		}
	}

	// Filler headcount so the response-rate denominator is realistic.
	buckets := []string{"<1y", "1-3y", "3-5y", "5y+"}
	for _, d := range departmentSeed {
		for i := range d.Respondents {
			user := models.User{
				ID:           fmt.Sprintf("u-%s-%d", d.ID, i),
				Email:        fmt.Sprintf("%s.%d@mtsense.local", d.ID, i),
				PasswordHash: string(hash),
				FullName:     fmt.Sprintf("พนักงาน %s %d", d.EN, i+1),
				Role:         models.RoleEmployee,
				DepartmentID: d.ID,
				TenureBucket: buckets[i%len(buckets)],
				LastLoginAt:  time.Now().Add(-48 * time.Hour),
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

const demoSurveyID = "demo-1"

func seedSurvey(tx *gorm.DB) error {
	survey := models.Survey{
		ID:            demoSurveyID,
		Title:         models.Localized{TH: "แบบสำรวจประสบการณ์การทำงาน", EN: "Employee Experience Survey"},
		Cadence:       "monthly",
		NextRoundDate: time.Now().AddDate(0, 1, 0).Format("2006-01-02"),
		Status:        "published",
		Steps: []models.SurveyStep{
			{
				ID: "step-1", SurveyID: demoSurveyID, SortOrder: 0, EstimatedMinutes: 4,
				Title: models.Localized{TH: "บรรยากาศในที่ทำงาน", EN: "Workplace atmosphere"},
				Questions: []models.SurveyQuestion{
					{ID: "q1", StepID: "step-1", SortOrder: 0, Type: models.QuestionScale5, Required: true, MetricMapping: "satisfaction", TopicID: "work",
						Text: models.Localized{TH: "คุณพอใจกับภาระงานปัจจุบันแค่ไหน?", EN: "How satisfied are you with your current workload?"}},
					{ID: "q2", StepID: "step-1", SortOrder: 1, Type: models.QuestionScale5, Required: true, MetricMapping: "satisfaction", TopicID: "team",
						Text: models.Localized{TH: "คุณพอใจกับการทำงานร่วมกับทีมแค่ไหน?", EN: "How satisfied are you working with your team?"}},
					{ID: "q3", StepID: "step-1", SortOrder: 2, Type: models.QuestionENPS, Required: true, MetricMapping: "eNPS",
						Text: models.Localized{TH: "คุณจะแนะนำให้เพื่อนมาทำงานที่นี่มากแค่ไหน (0-10)?", EN: "How likely are you to recommend working here (0-10)?"}},
				},
			},
			{
				ID: "step-2", SurveyID: demoSurveyID, SortOrder: 1, EstimatedMinutes: 3,
				Title: models.Localized{TH: "ทีมและหัวหน้างาน", EN: "Team & management"},
				Questions: []models.SurveyQuestion{
					{ID: "q4", StepID: "step-2", SortOrder: 0, Type: models.QuestionScale5, Required: true, MetricMapping: "satisfaction", TopicID: "manager",
						Text: models.Localized{TH: "คุณพอใจกับการสนับสนุนจากหัวหน้าแค่ไหน?", EN: "How satisfied are you with your manager's support?"}},
					{ID: "q5", StepID: "step-2", SortOrder: 1, Type: models.QuestionScale5, Required: true, MetricMapping: "satisfaction", TopicID: "compensation",
						Text: models.Localized{TH: "คุณพอใจกับค่าตอบแทนแค่ไหน?", EN: "How satisfied are you with your compensation?"}},
					{ID: "q6", StepID: "step-2", SortOrder: 2, Type: models.QuestionSingleChoice, Required: true,
						Text:    models.Localized{TH: "คุณได้รับฟีดแบ็กจากหัวหน้าบ่อยแค่ไหน?", EN: "How often do you receive feedback from your manager?"},
						Options: []models.Localized{{TH: "ทุกสัปดาห์", EN: "Weekly"}, {TH: "ทุกเดือน", EN: "Monthly"}, {TH: "ไม่บ่อยนัก", EN: "Rarely"}}},
				},
			},
			{
				ID: "step-3", SurveyID: demoSurveyID, SortOrder: 2, EstimatedMinutes: 3,
				Title: models.Localized{TH: "ความคิดเห็นเพิ่มเติม", EN: "Additional feedback"},
				Questions: []models.SurveyQuestion{
					{ID: "q7", StepID: "step-3", SortOrder: 0, Type: models.QuestionScale5, Required: false, MetricMapping: "satisfaction", TopicID: "growth",
						Text: models.Localized{TH: "คุณพอใจกับโอกาสเติบโตในสายงานแค่ไหน?", EN: "How satisfied are you with your growth opportunities?"}},
					{ID: "q8", StepID: "step-3", SortOrder: 1, Type: models.QuestionScale5, Required: false, MetricMapping: "satisfaction", TopicID: "benefits",
						Text: models.Localized{TH: "คุณพอใจกับสวัสดิการแค่ไหน?", EN: "How satisfied are you with your benefits?"}},
					{ID: "q9", StepID: "step-3", SortOrder: 2, Type: models.QuestionOpenText, Required: false, SendToAI: true, AllowPublish: true,
						Text:           models.Localized{TH: "มีอะไรอยากบอกเพิ่มเติมไหม?", EN: "Anything else you would like to share?"},
						TagSuggestions: []string{"ภาระงาน", "สวัสดิการ", "สื่อสาร"}},
				},
			},
		},
	}
	return tx.Create(&survey).Error
}

// departmentProfile is the mean score per topic used to generate plausible answers,
// taken from the frontend's heatmap mock so the seeded dashboards look familiar.
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

// topicQuestion maps each scale question to the dimension it measures.
var topicQuestion = []struct {
	QuestionID string
	TopicID    string
	Index      int
}{
	{"q1", "work", 0},
	{"q2", "team", 1},
	{"q4", "manager", 2},
	{"q5", "compensation", 3},
	{"q7", "growth", 4},
	{"q8", "benefits", 5},
}

var openTextSamples = []string{
	"อยากให้มีการกระจายงานกะดึกให้เท่ากันมากขึ้น ตอนนี้บางคนหนักกว่าคนอื่นมาก",
	"สวัสดิการด้านสุขภาพจิตดีขึ้นมากในปีนี้ ขอบคุณที่รับฟัง",
	"อยากเห็นเส้นทางเติบโตที่ชัดเจนกว่านี้ ตอนนี้ไม่รู้ว่าต้องทำอะไรถึงจะโตในสายงาน",
	"ทีมดีมากค่ะ บรรยากาศการทำงานสนุก ช่วยเหลือกันดี",
	"ภาระงานหนักเกินไปในช่วงปิดไตรมาส อยากให้วางแผนกำลังคนล่วงหน้า",
	"การสื่อสารนโยบายยังไม่ชัดเจน บางครั้งรู้ข่าวช้ากว่าทีมอื่น",
}

// seedResponses writes six months of anonymous submissions.
func seedResponses(tx *gorm.DB) error {
	rng := rand.New(rand.NewSource(20260814))
	buckets := []string{"<1y", "1-3y", "3-5y", "5y+"}
	now := time.Now()

	for monthsAgo := 5; monthsAgo >= 0; monthsAgo-- {
		monthStart := now.AddDate(0, -monthsAgo, 0)
		period := monthStart.Format("2006-01")
		// A gentle upward drift so the six-month trend line has a direction.
		drift := float64(5-monthsAgo) * 0.06

		for _, d := range departmentSeed {
			// Roughly three quarters of each department responds.
			responding := max(1, d.Respondents*3/4)
			profile := departmentProfile[d.ID]

			for i := range responding {
				response := models.SurveyResponse{
					ID:             uuid.NewString(),
					SurveyID:       demoSurveyID,
					AnonymousToken: uuid.NewString(),
					DepartmentID:   d.ID,
					TenureBucket:   buckets[i%len(buckets)],
					SubmittedAt:    monthStart.AddDate(0, 0, -rng.Intn(20)),
					PeriodMonth:    period,
				}
				if err := tx.Create(&response).Error; err != nil {
					return err
				}

				answers := make([]models.ResponseAnswer, 0, len(topicQuestion)+2)
				for _, tq := range topicQuestion {
					score := clamp(profile[tq.Index]+drift+rng.NormFloat64()*0.6, 1, 5)
					rounded := float64(int(score + 0.5))
					answers = append(answers, models.ResponseAnswer{
						ResponseID:   response.ID,
						QuestionID:   tq.QuestionID,
						TopicID:      tq.TopicID,
						NumericValue: &rounded,
						Value:        []byte(fmt.Sprintf("%d", int(rounded))),
					})
				}

				// eNPS, loosely correlated with how the department feels overall.
				base := (profile[0] + profile[1] + profile[2]) / 3
				enps := clamp((base+drift-1)/4*10+rng.NormFloat64()*1.8, 0, 10)
				enpsRounded := float64(int(enps + 0.5))
				answers = append(answers, models.ResponseAnswer{
					ResponseID:   response.ID,
					QuestionID:   "q3",
					NumericValue: &enpsRounded,
					Value:        []byte(fmt.Sprintf("%d", int(enpsRounded))),
				})

				// One in five leaves open text, already redacted at write time.
				if rng.Intn(5) == 0 {
					text := openTextSamples[rng.Intn(len(openTextSamples))]
					answers = append(answers, models.ResponseAnswer{
						ResponseID:   response.ID,
						QuestionID:   "q9",
						Value:        []byte(fmt.Sprintf("%q", text)),
						TextRedacted: text,
						Sentiment:    seedSentiment(rng),
					})
				}

				if err := tx.Create(&answers).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func seedSentiment(rng *rand.Rand) string {
	switch n := rng.Intn(100); {
	case n < 58:
		return "positive"
	case n < 87:
		return "neutral"
	default:
		return "negative"
	}
}

func seedAnalysisOutput(tx *gorm.DB) error {
	period := time.Now().Format("2006-01")

	insight := models.AIInsight{
		PeriodMonth:  period,
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
		{ID: "urg-1", PeriodMonth: period, Rank: 1, DepartmentID: "support",
			Label:          models.Localized{TH: "ภาระงาน", EN: "Workload"},
			DepartmentName: models.Localized{TH: "ฝ่ายซัพพอร์ต", EN: "Support"}},
		{ID: "urg-2", PeriodMonth: period, Rank: 2, DepartmentID: "marketing",
			Label:          models.Localized{TH: "ค่าตอบแทน", EN: "Compensation"},
			DepartmentName: models.Localized{TH: "ฝ่ายการตลาด", EN: "Marketing"}},
		{ID: "urg-3", PeriodMonth: period, Rank: 3, DepartmentID: "",
			Label:          models.Localized{TH: "การสื่อสารนโยบาย", EN: "Policy communication"},
			DepartmentName: models.Localized{TH: "ทั้งองค์กร", EN: "Company-wide"}},
	}
	if err := tx.Create(&urgent).Error; err != nil {
		return err
	}

	decisions := []models.DecisionItem{
		{ID: "dec-1", PeriodMonth: period, Rank: 1, Severity: 82, Label: models.Localized{TH: "ภาระงานฝ่ายซัพพอร์ต", EN: "Support team workload"}},
		{ID: "dec-2", PeriodMonth: period, Rank: 2, Severity: 58, Label: models.Localized{TH: "เส้นทางเติบโต", EN: "Career growth paths"}},
		{ID: "dec-3", PeriodMonth: period, Rank: 3, Severity: 50, Label: models.Localized{TH: "การสื่อสารนโยบาย", EN: "Policy communication"}},
	}
	if err := tx.Create(&decisions).Error; err != nil {
		return err
	}

	terms := []models.WordCloudTerm{
		{PeriodMonth: period, Frequency: 92, TopicID: "work", Term: models.Localized{TH: "ภาระงาน", EN: "Workload"}},
		{PeriodMonth: period, Frequency: 71, TopicID: "manager", Term: models.Localized{TH: "หัวหน้าทีม", EN: "Team lead"}},
		{PeriodMonth: period, Frequency: 66, TopicID: "benefits", Term: models.Localized{TH: "สวัสดิการ", EN: "Benefits"}},
		{PeriodMonth: period, Frequency: 54, TopicID: "growth", Term: models.Localized{TH: "เติบโต", EN: "Growth"}},
		{PeriodMonth: period, Frequency: 48, TopicID: "compensation", Term: models.Localized{TH: "ค่าตอบแทน", EN: "Compensation"}},
		{PeriodMonth: period, Frequency: 39, TopicID: "work", Term: models.Localized{TH: "กะดึก", EN: "Night shift"}},
		{PeriodMonth: period, Frequency: 33, TopicID: "team", Term: models.Localized{TH: "ทีม", EN: "Team"}},
		{PeriodMonth: period, Frequency: 28, TopicID: "growth", Term: models.Localized{TH: "สื่อสาร", EN: "Communication"}},
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
		{ID: "sum-1", PublishedAt: time.Now().AddDate(0, 0, -13).Format("2006-01-02"),
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
		{ID: "act-1", TopicID: "benefits", Assignee: "ฝ่ายทรัพยากรบุคคล", Status: "done", CreatedBy: models.RoleHR, Level: "full",
			TargetDate: time.Now().AddDate(0, -1, 0).Format("2006-01-02"),
			Topic:      models.Localized{TH: "เพิ่มวันลาพักใจ 2 วัน/ปี", EN: "Added 2 wellness days per year"}},
		{ID: "act-2", TopicID: "work", Assignee: "หัวหน้าฝ่ายซัพพอร์ต", Status: "in_progress", CreatedBy: models.RoleHR, Level: "full",
			TargetDate: time.Now().AddDate(0, 1, 0).Format("2006-01-02"),
			Topic:      models.Localized{TH: "ทบทวนตารางกะดึก", EN: "Reviewing night-shift scheduling"}},
		{ID: "act-3", TopicID: "manager", Assignee: "ฝ่ายทรัพยากรบุคคล", Status: "in_progress", CreatedBy: models.RoleExecutive, Level: "decision",
			TargetDate: time.Now().AddDate(0, 2, 0).Format("2006-01-02"),
			Topic:      models.Localized{TH: "อบรมหัวหน้าทีมเรื่องการให้ฟีดแบ็ก", EN: "Manager feedback training"}},
	}
	if err := tx.Create(&actions).Error; err != nil {
		return err
	}

	// Feed posts covering every gate combination, matching src/mocks/feed.ts.
	today := time.Now()
	posts := []models.FeedPost{
		{ID: "f1", Text: "อยากให้มีการกระจายงานกะดึกให้เท่ากันมากขึ้น ตอนนี้บางคนหนักกว่าคนอื่นมาก",
			Hashtags: []string{"ภาระงาน", "กะดึก"}, PostedOn: today.AddDate(0, 0, -9).Format("2006-01-02"),
			Upvotes: 142, Downvotes: 4, HRReplied: true, OptedIn: true, Published: true},
		{ID: "f2", Text: "สวัสดิการด้านสุขภาพจิตดีขึ้นมากในปีนี้ ขอบคุณที่รับฟัง",
			Hashtags: []string{"สวัสดิการ"}, PostedOn: today.AddDate(0, 0, -10).Format("2006-01-02"),
			Upvotes: 98, Downvotes: 1, OptedIn: true, Published: true},
		{ID: "f3", Text: "อยากเห็นเส้นทางเติบโตที่ชัดเจนกว่านี้ ตอนนี้ไม่รู้ว่าต้องทำอะไรถึงจะโตในสายงาน",
			Hashtags: []string{"เติบโต"}, PostedOn: today.AddDate(0, 0, -11).Format("2006-01-02"),
			Upvotes: 76, Downvotes: 2, HRReplied: true, OptedIn: true, Published: true},
		{ID: "f4", Text: "ทีมดีมากค่ะ บรรยากาศการทำงานสนุก ช่วยเหลือกันดี",
			Hashtags: []string{"ทีม"}, PostedOn: today.AddDate(0, 0, -12).Format("2006-01-02"),
			Upvotes: 54, OptedIn: true, Published: true},
		// Opted in but not yet moderated — must NOT appear in the public feed.
		{ID: "f5", Text: "ข้อความนี้เพิ่งส่งเข้าระบบ อยู่ระหว่างตรวจสอบก่อนเผยแพร่",
			Hashtags: []string{"ภาระงาน"}, PostedOn: today.Format("2006-01-02"),
			OptedIn: true, Published: false},
		// Author never opted in — must NOT appear regardless of moderation.
		{ID: "f6", Text: "ความเห็นนี้ผู้ตอบไม่ได้เลือกเผยแพร่ จึงไม่ปรากฏใน feed",
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
