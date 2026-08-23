package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mt-sense/backend-service/internal/models"
)

// The DTO layer exists to keep the wire format stable and to keep persistence off the
// wire. These tests assert both, because a silent change to either breaks the Vue client
// or leaks a column.

func TestUserDTOOmitsCredentials(t *testing.T) {
	deptID := "hr"
	posID := "manager"
	user := &models.User{
		ID:           "u-hr",
		Email:        "hr@mtsense.local",
		PasswordHash: "$2a$10$SUPERSECRETHASH",
		FullName:     "พิมพ์ชนก ศรีสุข",
		Role:         models.RoleAdmin,
		DepartmentID: &deptID,
		PositionID:   &posID,
		LastLoginAt:  time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
	}

	encoded, err := json.Marshal(NewUser(user, "ฝ่ายทรัพยากรบุคคล", "ผู้จัดการ"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)

	for _, secret := range []string{"SUPERSECRETHASH", "passwordHash", "PasswordHash"} {
		if strings.Contains(body, secret) {
			t.Errorf("user DTO leaked %q: %s", secret, body)
		}
	}
	// department/position must be the resolved names, not the raw ids.
	if !strings.Contains(body, "ฝ่ายทรัพยากรบุคคล") {
		t.Errorf("department name missing: %s", body)
	}
	if !strings.Contains(body, "ผู้จัดการ") {
		t.Errorf("position name missing: %s", body)
	}
}

// The frontend's Suppressible<T> is a discriminated union; both arms must serialize
// exactly as its type declares or the client fails to narrow.
func TestSuppressibleArmsMatchFrontendUnion(t *testing.T) {
	hidden, err := json.Marshal(HeatmapCell{TopicID: "work", Score: models.Hidden[float64]()})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"topicId":"work","score":{"suppressed":true}}`; string(hidden) != want {
		t.Errorf("suppressed cell = %s, want %s", hidden, want)
	}

	shown, err := json.Marshal(HeatmapCell{TopicID: "work", Score: models.Visible(3.9)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"topicId":"work","score":{"suppressed":false,"data":3.9}}`; string(shown) != want {
		t.Errorf("visible cell = %s, want %s", shown, want)
	}
}

// A suppressed cell must carry no numeric field at all — not a zero, not a null. A zero
// would render as a real score of 0.0 in the heatmap.
func TestSuppressedCellCarriesNoNumber(t *testing.T) {
	encoded, _ := json.Marshal(DepartmentScore{DepartmentID: "innovation", Score: models.Hidden[float64]()})
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	score := generic["score"].(map[string]any)
	if _, present := score["data"]; present {
		t.Errorf("suppressed score still carries a data field: %s", encoded)
	}
}

// ExecutiveSummary is the aggregate-only payload. If a field is ever added that could
// carry respondent text, this test is where it should be caught.
func TestExecutiveSummaryHasNoTextFields(t *testing.T) {
	encoded, err := json.Marshal(ExecutiveSummary{})
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}

	allowed := map[string]bool{
		"score": true, "sentiment": true, "radar": true,
		"departmentComparison": true, "positionComparison": true, "decisionItems": true,
	}
	for key := range generic {
		if !allowed[key] {
			t.Errorf("unexpected field %q on ExecutiveSummary — executives must receive aggregates only", key)
		}
	}
}

func TestSubmitResponseRequestValidatesScoreRange(t *testing.T) {
	tooLow := SubmitResponseRequest{SatisfactionScore: 0}
	if problems := tooLow.Validate(); len(problems) == 0 {
		t.Error("score 0 should be rejected")
	}

	tooHigh := SubmitResponseRequest{SatisfactionScore: 6}
	if problems := tooHigh.Validate(); len(problems) == 0 {
		t.Error("score 6 should be rejected")
	}

	valid := SubmitResponseRequest{SatisfactionScore: 4, CommentText: "ดีมาก"}
	if problems := valid.Validate(); len(problems) > 0 {
		t.Errorf("valid score rejected: %v", problems)
	}
}

func TestSubmitResponseReceiptHasNoUserField(t *testing.T) {
	encoded, err := json.Marshal(SubmitResponseReceipt{SubmittedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	for key := range generic {
		if key != "submittedAt" {
			t.Errorf("unexpected field %q on submission receipt — must never identify the submitter", key)
		}
	}
}

// Empty collections must serialize as [] rather than null; the Vue components iterate
// these directly and null would throw.
func TestEmptyCollectionsSerializeAsArrays(t *testing.T) {
	cases := map[string]any{
		"topics":       NewTopics(nil),
		"departments":  NewDepartments(nil, nil),
		"positions":    NewPositions(nil),
		"feedPosts":    NewFeedPosts(nil),
		"actionItems":  NewActionItems(nil),
		"summaries":    NewPublishedSummaries(nil),
		"wordcloud":    NewWordCloud(nil),
		"subIssues":    NewSubIssues(nil),
		"alerts":       NewAlerts(nil),
		"periods":      NewSurveyPeriodList(nil, nil),
		"submissions":  NewSubmissionHistory(nil, nil),
	}
	for name, value := range cases {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(encoded) != "[]" {
			t.Errorf("%s serialized as %s, want []", name, encoded)
		}
	}
}

func TestUpdateSettingsOnlyAllowsNotificationColumns(t *testing.T) {
	yes := true
	req := UpdateSettingsRequest{NotifyNewRound: &yes}
	updates := req.Updates()

	if len(updates) != 1 || updates["notify_new_round"] != true {
		t.Errorf("updates = %v, want only notify_new_round", updates)
	}
	// Nothing supplied means nothing written — the handler turns this into a 400.
	if got := (&UpdateSettingsRequest{}).Updates(); len(got) != 0 {
		t.Errorf("empty request produced updates: %v", got)
	}
}
