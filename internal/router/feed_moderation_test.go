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

func TestFeedModerationAndReply(t *testing.T) {
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
	cfg := &config.Config{JWTSecret: strings.Repeat("b", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)

	org := uuid.NewString()
	if err := db.Create(&models.Organization{ID: org, Name: "Feed Co", Slug: org, JoinCode: org[:6]}).Error; err != nil {
		t.Fatal(err)
	}
	hrTok, _, _ := issuer.AccessToken(uuid.NewString(), org, models.RoleAdmin)
	empTok, _, _ := issuer.AccessToken(uuid.NewString(), org, models.RoleEmployee)

	keep, drop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{keep, drop} {
		if err := db.Create(&models.FeedPost{ID: id, OrgID: org, Text: "post " + id[:4], PostedOn: "2026-10-26", OptedIn: true}).Error; err != nil {
			t.Fatal(err)
		}
	}

	call := func(method, path, tok string, body any) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	pendingIDs := func() string {
		_, body := call("GET", "/api/feed/pending", hrTok, nil)
		return string(body)
	}

	if s, _ := call("PATCH", "/api/feed/"+keep+"/moderate", empTok, map[string]any{"published": true}); s != 403 {
		t.Fatalf("employee moderate status = %d", s)
	}

	// Rejecting removes the post from the pending queue without publishing it.
	if s, b := call("PATCH", "/api/feed/"+drop+"/moderate", hrTok, map[string]any{"published": false}); s != 200 {
		t.Fatalf("reject: %d %s", s, b)
	}
	if p := pendingIDs(); strings.Contains(p, drop) || !strings.Contains(p, keep) {
		t.Fatalf("pending after reject = %s", p)
	}

	// Reply + publish: the reply is public in the feed and sets the badge.
	if s, b := call("PATCH", "/api/feed/"+keep+"/moderate", hrTok, map[string]any{"published": true, "hrReply": "  HR รับเรื่องแล้ว  "}); s != 200 {
		t.Fatalf("publish: %d %s", s, b)
	}
	_, body := call("GET", "/api/feed", empTok, nil)
	var feed struct {
		Posts []struct {
			ID, HRReply, HRRepliedAt string
			HRReplied                bool
		} `json:"posts"`
	}
	_ = json.Unmarshal(body, &feed)
	if len(feed.Posts) != 1 || feed.Posts[0].ID != keep || feed.Posts[0].HRReply != "HR รับเรื่องแล้ว" ||
		!feed.Posts[0].HRReplied || feed.Posts[0].HRRepliedAt == "" {
		t.Fatalf("feed = %s", body)
	}

	// Clearing the reply also clears the badge.
	if s, b := call("PATCH", "/api/feed/"+keep+"/moderate", hrTok, map[string]any{"hrReply": ""}); s != 200 ||
		!strings.Contains(string(b), `"hrReplied":false`) || !strings.Contains(string(b), `"hrReply":""`) {
		t.Fatalf("clear reply: %d %s", s, b)
	}

	if s, _ := call("PATCH", "/api/feed/"+keep+"/moderate", hrTok, map[string]any{"hrReply": strings.Repeat("x", 2001)}); s != 400 {
		t.Fatalf("over-long reply status = %d", s)
	}
}
