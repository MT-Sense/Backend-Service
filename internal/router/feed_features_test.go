package router

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/models"
)

// TestFeedFeatures covers vote toggling, HR summaries (drafts hidden from employees), action
// item permissions and the drilldown's "why is this hidden" reason.
func TestFeedFeatures(t *testing.T) {
	dsn := os.Getenv("FEED_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FEED_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{JWTSecret: strings.Repeat("c", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)

	org := uuid.NewString()
	if err := db.Create(&models.Organization{ID: org, Name: "Features Co", Slug: org, JoinCode: org[:6]}).Error; err != nil {
		t.Fatal(err)
	}
	tok := func(role models.Role) string {
		s, _, _ := issuer.AccessToken(uuid.NewString(), org, role)
		return s
	}
	hr, exec, otherExec, emp, emp2 := tok(models.RoleAdmin), tok(models.RoleExecutive), tok(models.RoleExecutive), tok(models.RoleEmployee), tok(models.RoleEmployee)

	call := func(method, path, token string, body any) (int, map[string]any, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		return resp.StatusCode, obj, raw
	}

	// --- votes: up, up again (undo), down, switch, and myVote per caller ---
	post := uuid.NewString()
	if err := db.Create(&models.FeedPost{ID: post, OrgID: org, Text: "vote me", PostedOn: "2026-10-26", OptedIn: true, Published: true}).Error; err != nil {
		t.Fatal(err)
	}
	votePath := "/api/feed/" + post + "/vote"
	expectVote := func(token string, dir int, up, down, mine float64) {
		t.Helper()
		s, p, raw := call("POST", votePath, token, map[string]any{"direction": dir})
		if s != 200 || p["upvotes"] != up || p["downvotes"] != down || p["myVote"] != mine {
			t.Fatalf("vote %d: %d %s (want up=%v down=%v mine=%v)", dir, s, raw, up, down, mine)
		}
	}
	expectVote(emp, 1, 1, 0, 1)
	expectVote(emp, 1, 0, 0, 0) // same direction again removes the vote
	expectVote(emp, -1, 0, 1, -1)
	expectVote(emp, 1, 1, 0, 1) // switching moves the vote
	expectVote(emp2, -1, 1, 1, -1)
	_, list, _ := call("GET", "/api/feed", emp, nil)
	if got := list["posts"].([]any)[0].(map[string]any)["myVote"]; got != float64(1) {
		t.Fatalf("emp myVote in list = %v", got)
	}
	_, list, _ = call("GET", "/api/feed", hr, nil)
	if got := list["posts"].([]any)[0].(map[string]any)["myVote"]; got != float64(0) {
		t.Fatalf("hr myVote in list = %v (must not see others' votes)", got)
	}

	// --- summaries: drafts stay HR-only; publishing stamps a date; validation ---
	if s, _, _ := call("POST", "/api/hr/summaries", emp, map[string]any{}); s != 403 {
		t.Fatalf("employee create summary = %d", s)
	}
	if s, _, raw := call("POST", "/api/hr/summaries", hr, map[string]any{"title": map[string]string{"th": ""}}); s != 400 || !strings.Contains(string(raw), "title (Thai)") {
		t.Fatalf("invalid summary = %d %s", s, raw)
	}
	s, draft, raw := call("POST", "/api/hr/summaries", hr, map[string]any{
		"title": map[string]string{"th": "สรุปรอบนี้"}, "body": map[string]string{"th": "ภาระงานเป็นเรื่องหลัก"},
	})
	if s != 201 || draft["status"] != "draft" || draft["publishedAt"] != "" {
		t.Fatalf("create draft = %d %s", s, raw)
	}
	_, _, pub := call("GET", "/api/summaries", emp, nil)
	if strings.Contains(string(pub), "สรุปรอบนี้") {
		t.Fatal("draft leaked to the employee feed")
	}
	id := draft["id"].(string)
	s, published, raw := call("PATCH", "/api/hr/summaries/"+id, hr, map[string]any{
		"title": map[string]string{"th": "สรุปรอบนี้", "en": "This round"}, "body": map[string]string{"th": "ภาระงานเป็นเรื่องหลัก"}, "status": "published",
	})
	if s != 200 || published["status"] != "published" || published["publishedAt"] == "" {
		t.Fatalf("publish = %d %s", s, raw)
	}
	_, _, pub = call("GET", "/api/summaries", emp, nil)
	if !strings.Contains(string(pub), "This round") {
		t.Fatalf("published summary missing from feed: %s", pub)
	}
	if s, _, _ := call("DELETE", "/api/hr/summaries/"+id, hr, nil); s != 204 {
		t.Fatalf("delete summary = %d", s)
	}

	// --- action items: exec edits only their own, HR edits any, done_at follows status ---
	topic := map[string]any{"topic": map[string]string{"th": "งาน", "en": "Work"}, "topicId": "work", "targetDate": "2026-12-31"}
	_, execItem, _ := call("POST", "/api/action-items", exec, topic)
	_, hrItem, _ := call("POST", "/api/action-items", hr, topic)
	if execItem["createdByMe"] != true || execItem["assignee"] != "TBD" {
		t.Fatalf("exec item = %v", execItem)
	}
	if s, _, _ := call("POST", "/api/action-items", hr, map[string]any{"topic": map[string]string{"th": "x"}, "targetDate": "31/12/2026"}); s != 400 {
		t.Fatalf("bad date accepted: %d", s)
	}
	if s, _, _ := call("PATCH", "/api/action-items/"+hrItem["id"].(string), exec, map[string]any{"status": "done"}); s != 403 {
		t.Fatalf("exec editing HR item = %d", s)
	}
	if s, _, _ := call("PATCH", "/api/action-items/"+execItem["id"].(string), otherExec, map[string]any{"status": "done"}); s != 403 {
		t.Fatalf("other exec editing = %d", s)
	}
	s, done, raw := call("PATCH", "/api/action-items/"+execItem["id"].(string), exec, map[string]any{"status": "done", "assignee": "ทีมบริหาร"})
	if s != 200 || done["status"] != "done" || done["doneAt"] == "" || done["assignee"] != "ทีมบริหาร" {
		t.Fatalf("exec closes own item = %d %s", s, raw)
	}
	s, reopened, _ := call("PATCH", "/api/action-items/"+execItem["id"].(string), hr, map[string]any{"status": "in_progress"})
	if s != 200 || reopened["doneAt"] != "" {
		t.Fatalf("HR reopen = %d %v", s, reopened)
	}
	if s, _, _ := call("PATCH", "/api/action-items/"+hrItem["id"].(string), emp, map[string]any{"status": "done"}); s != 403 {
		t.Fatalf("employee edit = %d", s)
	}
	if s, _, _ := call("DELETE", "/api/action-items/"+hrItem["id"].(string), hr, nil); s != 204 {
		t.Fatalf("HR delete = %d", s)
	}

	// --- link an action item to a post, shown in the feed; deleting the item unlinks ---
	linkPath := "/api/feed/" + post + "/moderate"
	itemID := execItem["id"].(string)
	if s, _, _ := call("PATCH", linkPath, hr, map[string]any{"actionItemId": uuid.NewString()}); s != 400 {
		t.Fatalf("unknown action item accepted: %d", s)
	}
	s, linked, raw := call("PATCH", linkPath, hr, map[string]any{"hrReply": "รับเรื่องแล้ว", "actionItemId": itemID})
	if s != 200 || linked["actionItem"] == nil || linked["actionItem"].(map[string]any)["id"] != itemID {
		t.Fatalf("link = %d %s", s, raw)
	}
	_, feed, _ := call("GET", "/api/feed", emp, nil)
	got := feed["posts"].([]any)[0].(map[string]any)["actionItem"].(map[string]any)
	if got["status"] != "in_progress" || got["topic"].(map[string]any)["th"] != "งาน" {
		t.Fatalf("feed action item = %v", got)
	}
	if _, ok := got["createdByMe"]; ok {
		t.Fatal("linked action item exposes creator details")
	}
	if s, _, _ := call("DELETE", "/api/action-items/"+itemID, hr, nil); s != 204 {
		t.Fatalf("delete linked item = %d", s)
	}
	_, feed, _ = call("GET", "/api/feed", emp, nil)
	if feed["posts"].([]any)[0].(map[string]any)["actionItem"] != nil {
		t.Fatal("link survived deleting the action item")
	}

	// --- drilldown: 404 carries the reason ---
	// The topics lookup is normally seeded; an isolated test database may not have it.
	if err := db.Where(models.Topic{ID: "work"}).FirstOrCreate(&models.Topic{ID: "work", Label: models.Localized{TH: "งาน", EN: "Work"}}).Error; err != nil {
		t.Fatal(err)
	}
	period := uuid.NewString()
	if err := db.Create(&models.SurveyPeriod{ID: period, OrgID: org, Month: 10, Year: 2026, OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	_, why, raw := call("GET", "/api/dashboard/hr/topics/work?period="+period, hr, nil)
	if why["reason"] != "suppressed" {
		t.Fatalf("empty period reason = %s", raw)
	}
	if err := db.Create(&models.SurveyResponse{ID: uuid.NewString(), OrgID: org, PeriodID: period, SatisfactionScore: 3, CommentText: "x", SubmittedAt: time.Now(), AnalysisStatus: "pending"}).Error; err != nil {
		t.Fatal(err)
	}
	_, why, raw = call("GET", "/api/dashboard/hr/topics/work?period="+period, hr, nil)
	if why["reason"] != "analysis_pending" || why["pendingAnalysis"] != float64(1) {
		t.Fatalf("pending reason = %s", raw)
	}
}
