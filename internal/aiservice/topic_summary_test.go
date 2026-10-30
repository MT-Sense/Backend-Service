package aiservice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTopicSummarySafeErrorCodes(t *testing.T) {
	for _, item := range []struct{ body, want string }{
		{`{"detail":{"code":"summary_provider_request","message":"PRIVATE"}}`, "summary_provider_request"},
		{`{"detail":{"code":"summary_provider_quota","message":"PRIVATE"}}`, "summary_provider_quota"},
		{`{"detail":{"code":"summary_timeout"}}`, "summary_timeout"},
		{`{"detail":{"code":"summary_invalid_evidence"}}`, "summary_invalid_evidence"},
		{`{"detail":{"code":"PRIVATE untrusted provider text"}}`, "summary_unavailable"},
		{`{"detail":"PRIVATE credentials"}`, "summary_unavailable"},
		{`not json PRIVATE`, "summary_unavailable"},
	} {
		t.Run(item.want, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/topics/summarize" || r.Header.Get("X-Automation-Token") != "test-token" {
					t.Error("wrong destination or auth")
				}
				w.WriteHeader(502)
				fmt.Fprint(w, item.body)
			}))
			defer server.Close()
			c := New(server.URL, time.Second)
			_, err := c.SummarizeTopic(context.Background(), TopicSummaryRequest{Topic: "Synthetic test", Comments: []TopicComment{}}, "test-token")
			var serviceError *TopicSummaryError
			if !errors.As(err, &serviceError) || serviceError.Code != item.want {
				t.Fatalf("wrong code: %v", err)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("provider text leaked")
			}
		})
	}
}
