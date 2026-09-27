package automation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCredentialIsolation(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cipher, err := Encrypt(key, "org-a", "secret")
	if err != nil {
		t.Fatal(err)
	}
	value, err := Decrypt(key, "org-a", cipher)
	if err != nil || value != "secret" {
		t.Fatal("cannot decrypt")
	}
	if _, err := Decrypt(key, "org-b", cipher); err == nil {
		t.Fatal("cross-org decryption allowed")
	}
	if _, err := Encrypt("bad-key", "org-a", "secret"); err == nil {
		t.Fatal("bad key accepted")
	}
}
func TestJiraDestinationAndPayload(t *testing.T) {
	for _, site := range []string{"http://x.atlassian.net", "https://x.atlassian.net.evil.com", "https://127.0.0.1", "https://x.atlassian.net/path", "https://user@x.atlassian.net"} {
		if ValidSite(site) {
			t.Fatalf("allowed %s", site)
		}
	}
	j := NewJira("https://test.atlassian.net", "hr@example.test", "secret")
	calls := 0
	j.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/rest/api/3/issue" {
			t.Fatal(r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		fields := body["fields"].(map[string]any)
		if fields["summary"] != "Approved title" {
			t.Fatal(fields)
		}
		if _, ok := fields["assignee"]; ok {
			t.Fatal("unexpected assignee")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"key":"HR-1"}`))}, nil
	})
	if _, err := j.Create(context.Background(), "HR OR 1=1", "10001", "x", "x", "m"); err == nil || calls != 0 {
		t.Fatal("invalid project sent")
	}
	link, err := j.Create(context.Background(), "HR", "10001", "Approved title", "Approved body", "mtsense-test")
	if err != nil || link != "https://test.atlassian.net/browse/HR-1" || calls != 1 {
		t.Fatal(link, err, calls)
	}
}
