package router

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/models"
)

type jiraTestTransport func(*http.Request) (*http.Response, error)

func (f jiraTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAutomationWorkflow(t *testing.T) {
	dsn := os.Getenv("AUTOMATION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AUTOMATION_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	var plannerCalls atomic.Int32
	planner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plannerCalls.Add(1)
		if r.Header.Get("X-Automation-Token") != "planner-test" {
			t.Error("missing planner token")
		}
		fmt.Fprint(w, `{"drafts":[{"playbook":"training","problemFound":true,"title":"Training plan","rationale":"Need skills","draft":"Review training options","evidenceIds":["survey"],"missingData":["Catalog unavailable"]}]}`)
	}))
	defer planner.Close()
	cfg := &config.Config{JWTSecret: strings.Repeat("a", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour, AIServiceURL: planner.URL, AutomationServiceToken: "planner-test", AutomationEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}
	app := fiber.New()
	Register(app, db, cfg)
	org := uuid.NewString()
	otherOrg := uuid.NewString()
	period := uuid.NewString()
	user := uuid.NewString()
	for _, id := range []string{org, otherOrg} {
		if err := db.Create(&models.Organization{ID: id, Name: "Automation test", Slug: id, JoinCode: id[:6]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.SurveyPeriod{ID: period, OrgID: org, Month: 9, Year: 2026, OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)
	admin, _, _ := issuer.AccessToken(user, org, models.RoleAdmin)
	employee, _, _ := issuer.AccessToken(user, org, models.RoleEmployee)
	other, _, _ := issuer.AccessToken("other", otherOrg, models.RoleAdmin)
	call := func(method, path, token string, body any) (int, []byte) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, e := app.Test(req, -1)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	expect := func(want int, method, path, token string, body any) []byte {
		t.Helper()
		status, out := call(method, path, token, body)
		if status != want {
			t.Fatalf("%s %s => %d want %d: %s", method, path, status, want, out)
		}
		return out
	}
	root := "/api/hr/automation"

	// Routing prototype: configurable ownership, unknown cases and human-only transitions.
	dept := uuid.NewString()
	if err := db.Create(&models.Department{ID: dept, OrgID: org, Name: "IT owner"}).Error; err != nil {
		t.Fatal(err)
	}
	expect(403, "GET", root+"/cases", employee, nil)
	policy := map[string]any{"version": 0, "rules": []map[string]any{{"id": "it", "topic": "เครื่องมือ", "keywords": []string{"ระบบล่ม"}, "departmentId": dept, "enabled": true}}}
	expect(200, "PATCH", root+"/routing-policy", admin, policy)
	expect(409, "PATCH", root+"/routing-policy", admin, policy)
	expect(400, "PATCH", root+"/routing-policy", other, policy)
	var rc models.RoutingCase
	json.Unmarshal(expect(201, "POST", root+"/cases", admin, map[string]string{"title": "ระบบล่ม", "summary": "ขอให้ตรวจระบบภายใน"}), &rc)
	if rc.DepartmentID != dept || rc.Status != "triage" {
		t.Fatal("routing suggestion must await review", rc)
	}
	rp := root + "/cases/" + rc.ID
	expect(404, "POST", rp, other, map[string]any{"version": 1, "action": "route"})
	expect(409, "POST", rp, admin, map[string]any{"version": 1, "action": "complete", "note": "too early"})
	expect(400, "POST", rp, admin, map[string]any{"version": 1, "action": "route", "topic": "IT", "departmentId": "foreign", "note": "checked"})
	expect(200, "POST", rp, admin, map[string]any{"version": 1, "action": "route", "topic": "IT", "departmentId": dept, "note": "HR reviewed and contacted owner"})
	expect(409, "POST", rp, admin, map[string]any{"version": 1, "action": "route", "topic": "IT", "departmentId": dept, "note": "duplicate"})
	expect(200, "POST", rp, admin, map[string]any{"version": 2, "action": "retriage", "note": "review destination"})
	expect(200, "POST", rp, admin, map[string]any{"version": 3, "action": "route", "topic": "IT", "departmentId": dept, "note": "confirmed"})
	expect(200, "POST", rp, admin, map[string]any{"version": 4, "action": "acknowledge", "note": "owner confirmed by call"})
	json.Unmarshal(expect(200, "POST", rp, admin, map[string]any{"version": 5, "action": "complete", "note": "resolved"}), &rc)
	if rc.Status != "completed" || len(rc.History) != 6 {
		t.Fatal("missing routing history", rc)
	}
	json.Unmarshal(expect(201, "POST", root+"/cases", admin, map[string]string{"title": "หัวข้อใหม่", "summary": "เรื่องที่ยังไม่มีกฎ"}), &rc)
	if rc.DepartmentID != "" || rc.Status != "triage" {
		t.Fatal("unknown topic must await triage", rc)
	}
	expect(401, "GET", root+"/proposals", "", nil)
	expect(403, "POST", root+"/demo", employee, nil)
	b := expect(201, "POST", root+"/demo", admin, nil)
	var ps []models.AutomationProposal
	if err := json.Unmarshal(b, &ps); err != nil || len(ps) != 4 {
		t.Fatal(string(b), err)
	}
	p := ps[0]
	path := root + "/proposals/" + p.ID
	expect(404, "POST", path+"/review", other, map[string]any{"version": 1, "decision": "approve"})
	expect(409, "POST", path+"/execute", admin, map[string]any{"version": 1})
	expect(400, "POST", path+"/review", admin, map[string]any{"version": 1, "decision": "edit", "title": "x", "draft": "x", "action": "jira_task"})
	expect(200, "POST", path+"/review", admin, map[string]any{"version": 1, "decision": "edit", "title": "Edited", "draft": "Exact reviewed body", "action": "local_task"})
	expect(409, "POST", path+"/review", admin, map[string]any{"version": 1, "decision": "approve"})
	expect(200, "POST", path+"/review", admin, map[string]any{"version": 2, "decision": "approve"})
	expect(409, "POST", path+"/review", admin, map[string]any{"version": 3, "decision": "edit", "title": "late", "draft": "late", "action": "local_task"})
	expect(200, "POST", path+"/review", admin, map[string]any{"version": 3, "decision": "withdraw"})
	expect(200, "POST", path+"/review", admin, map[string]any{"version": 4, "decision": "approve"})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := call("POST", path+"/execute", admin, map[string]any{"version": 5})
			codes <- s
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("duplicate execution", counts)
	}
	expect(200, "POST", path+"/complete", admin, map[string]any{"version": 7, "note": "Finished test"})
	expect(409, "POST", path+"/execute", admin, map[string]any{"version": 8})
	history := expect(200, "GET", path+"/events", admin, nil)
	if !bytes.Contains(history, []byte("Exact reviewed body")) {
		t.Fatal("missing approval history")
	}
	path2 := root + "/proposals/" + ps[1].ID
	expect(200, "POST", path2+"/review", admin, map[string]any{"version": 1, "decision": "reject"})
	expect(409, "POST", path2+"/execute", admin, map[string]any{"version": 2})
	expect(200, "GET", root+"/proposals", other, nil)
	settings := map[string]any{"revision": 0, "minResponses": 5, "mentionPercent": 30, "jiraSite": "https://test.atlassian.net", "jiraEmail": "hr@example.test", "jiraProject": "HR", "jiraIssueType": "10001", "token": "never-return-me", "allowJiraWrite": true}
	saved := expect(200, "PATCH", root+"/settings", admin, settings)
	if bytes.Contains(saved, []byte("never-return-me")) {
		t.Fatal("token leaked")
	}
	expect(409, "PATCH", root+"/settings", admin, settings)
	var stored models.AutomationSettings
	db.First(&stored, "org_id = ?", org)
	if stored.JiraToken == "never-return-me" || stored.JiraToken == "" {
		t.Fatal("token not encrypted")
	}
	settings["revision"] = 1
	settings["jiraSite"] = "http://localhost:8000"
	expect(400, "PATCH", root+"/settings", admin, settings)
	settings["jiraSite"] = "https://test.atlassian.net"
	settings["minResponses"] = 4
	expect(400, "PATCH", root+"/settings", admin, settings)
	// Exercise actual Jira execution path using a recording transport, never a real Jira.
	originalTransport := http.DefaultTransport
	var writes atomic.Int32
	ambiguous := false
	http.DefaultTransport = jiraTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "test.atlassian.net" {
			return originalTransport.RoundTrip(r)
		}
		writes.Add(1)
		if ambiguous {
			return nil, errors.New("simulated lost response")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["fields"].(map[string]any)["summary"] != "Exact Jira title" {
			t.Error("wrong approved payload")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"key":"HR-99"}`)), Header: make(http.Header)}, nil
	})
	defer func() { http.DefaultTransport = originalTransport }()
	makeJiraProposal := func() models.AutomationProposal {
		id := uuid.NewString()
		row := models.AutomationProposal{ID: id, OrgID: org, ScopeKey: id, Status: "pending", Version: 1, Title: "Exact Jira title", Draft: "Approved Jira body", Action: "local_task"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	live := makeJiraProposal()
	livePath := root + "/proposals/" + live.ID
	expect(200, "POST", livePath+"/review", admin, map[string]any{"version": 1, "decision": "edit", "title": "Exact Jira title", "draft": "Approved Jira body", "action": "jira_task"})
	expect(200, "POST", livePath+"/review", admin, map[string]any{"version": 2, "decision": "approve"})
	if writes.Load() != 0 {
		t.Fatal("approval must not write to Jira", writes.Load())
	}
	expect(200, "POST", livePath+"/execute", admin, map[string]any{"version": 3})
	expect(409, "POST", livePath+"/execute", admin, map[string]any{"version": 3})
	if writes.Load() != 1 {
		t.Fatal("unexpected number of Jira writes", writes.Load())
	}
	ambiguous = true
	uncertain := makeJiraProposal()
	uncertainPath := root + "/proposals/" + uncertain.ID
	expect(200, "POST", uncertainPath+"/review", admin, map[string]any{"version": 1, "decision": "edit", "title": "Exact Jira title", "draft": "Approved Jira body", "action": "jira_task"})
	expect(200, "POST", uncertainPath+"/review", admin, map[string]any{"version": 2, "decision": "approve"})
	outcome := expect(200, "POST", uncertainPath+"/execute", admin, map[string]any{"version": 3})
	if !bytes.Contains(outcome, []byte("needs_check")) {
		t.Fatal("ambiguous external outcome not surfaced")
	}
	expect(409, "POST", uncertainPath+"/execute", admin, map[string]any{"version": 5})
	if writes.Load() != 2 {
		t.Fatal("ambiguous write retried")
	}
	blocked := makeJiraProposal()
	blockedPath := root + "/proposals/" + blocked.ID
	expect(200, "POST", blockedPath+"/review", admin, map[string]any{"version": 1, "decision": "edit", "title": "Exact Jira title", "draft": "Approved Jira body", "action": "jira_task"})
	expect(200, "POST", blockedPath+"/review", admin, map[string]any{"version": 2, "decision": "approve"})
	settings["minResponses"] = 5
	expect(200, "PATCH", root+"/settings", admin, settings)
	expect(409, "POST", blockedPath+"/execute", admin, map[string]any{"version": 3})
	if writes.Load() != 2 {
		t.Fatal("settings change did not invalidate execution")
	}
	http.DefaultTransport = originalTransport

	// Four analyzed comments cannot be sent to the planner; the fifth unlocks generation.
	for i := 0; i < 5; i++ {
		if i == 4 {
			expect(200, "POST", root+"/generate", admin, map[string]any{"periodId": period})
			if plannerCalls.Load() != 0 {
				t.Fatal("small group sent to model")
			}
		}
		id := uuid.NewString()
		if err := db.Create(&models.SurveyResponse{ID: id, OrgID: org, PeriodID: period, SatisfactionScore: 3, CommentText: "ต้องการอบรมเพิ่มเติม", SubmittedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.ResponseAnalysis{ID: uuid.NewString(), ResponseID: id, SentimentLabel: "neu", Confidence: 0.7, Categories: []string{"growth"}, AnalyzedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	expect(404, "POST", root+"/generate", other, map[string]any{"periodId": period})
	expect(200, "POST", root+"/generate", admin, map[string]any{"periodId": period})
	expect(200, "POST", root+"/generate", admin, map[string]any{"periodId": period})
	if plannerCalls.Load() != 1 {
		t.Fatalf("duplicate planning calls %d", plannerCalls.Load())
	}
	var n int64
	db.Model(&models.AutomationProposal{}).Where("org_id = ? AND period_id = ? AND demo = false", org, period).Count(&n)
	if n != 1 {
		t.Fatal("duplicate proposals", n)
	}
	manual := map[string]any{"revision": 2, "minResponses": 5, "mentionPercent": 30, "connectorType": "manual", "toolName": "ระบบงานที่ HR เลือก", "toolInstructions": "ส่งเฉพาะสรุปที่ตรวจแล้ว", "jiraSite": "https://test.atlassian.net", "jiraEmail": "hr@example.test", "jiraProject": "HR", "jiraIssueType": "10001", "allowJiraWrite": true}
	manualSaved := expect(200, "PATCH", root+"/settings", admin, manual)
	if !bytes.Contains(manualSaved, []byte("ระบบงานที่ HR เลือก")) {
		t.Fatal("manual tool settings not returned")
	}
	db.First(&stored, "org_id = ?", org)
	if stored.ConnectorType != "manual" || stored.AllowJiraWrite {
		t.Fatal("manual connector must not execute Jira", stored.ConnectorType, stored.AllowJiraWrite)
	}
	expect(400, "POST", root+"/jira/test", admin, nil)
}
