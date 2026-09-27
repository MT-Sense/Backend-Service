package automation

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var sitePattern = regexp.MustCompile(`^https://[a-z0-9][a-z0-9-]*\.atlassian\.net$`)
var ProjectPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,19}$`)
var IssueTypePattern = regexp.MustCompile(`^[0-9]{1,20}$`)

func ValidSite(site string) bool { return sitePattern.MatchString(site) }
func crypt(key string) (cipher.AEAD, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("AUTOMATION_ENCRYPTION_KEY must be base64 of 32 random bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func Encrypt(key, org, plain string) (string, error) {
	a, err := crypt(key)
	if err != nil {
		return "", err
	}
	n := make([]byte, a.NonceSize())
	if _, err = rand.Read(n); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(a.Seal(n, n, []byte(plain), []byte(org))), nil
}
func Decrypt(key, org, encoded string) (string, error) {
	a, err := crypt(key)
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(b) < a.NonceSize() {
		return "", errors.New("invalid encrypted credential")
	}
	p, err := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte(org))
	return string(p), err
}

type Jira struct {
	Site, Email, Token string
	HTTP               *http.Client
}

func NewJira(site, email, token string) *Jira {
	return &Jira{site, email, token, &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Only an exact Jira Cloud HTTPS host is accepted; no arbitrary webhook URLs, redirects,
// user-provided JQL, or model-provided API paths can receive this credential.
func (j *Jira) call(ctx context.Context, method, path string, body any, out any) error {
	if !ValidSite(j.Site) {
		return errors.New("invalid Jira Cloud site")
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, j.Site+path, r)
	if err != nil {
		return err
	}
	req.SetBasicAuth(j.Email, j.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := j.HTTP.Do(req)
	if err != nil {
		return errors.New("Jira connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Jira returned HTTP %d; check permissions and project settings", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
	}
	return nil
}
func (j *Jira) Test(ctx context.Context, project string) error {
	if !ProjectPattern.MatchString(project) {
		return errors.New("invalid project key")
	}
	return j.call(ctx, "GET", "/rest/api/3/project/"+url.PathEscape(project), nil, &map[string]any{})
}

// Snapshot deliberately fetches only issue keys, statuses and due dates, never customer
// descriptions, assignees, attachments, or untrusted issue instructions. It is current,
// not historical: the timestamp and sampling limit are included in the evidence.
func (j *Jira) Snapshot(ctx context.Context, project string) (string, error) {
	if !ProjectPattern.MatchString(project) {
		return "", errors.New("invalid project key")
	}
	var result struct {
		IsLast bool `json:"isLast"`
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				DueDate string `json:"duedate"`
				Status  struct {
					Name string `json:"name"`
				} `json:"status"`
			} `json:"fields"`
		} `json:"issues"`
	}
	body := map[string]any{"jql": "project = " + project + " AND statusCategory != Done ORDER BY updated DESC", "fields": []string{"status", "duedate"}, "maxResults": 100}
	if err := j.call(ctx, "POST", "/rest/api/3/search/jql", body, &result); err != nil {
		return "", err
	}
	overdue := 0
	states := map[string]int{}
	today := time.Now().UTC().Format("2006-01-02")
	for _, i := range result.Issues {
		states[i.Fields.Status.Name]++
		if i.Fields.DueDate != "" && i.Fields.DueDate < today {
			overdue++
		}
	}
	b, _ := json.Marshal(states)
	return fmt.Sprintf("Current Jira snapshot at %s; project %s; open issues in sample %d; overdue in sample %d; status counts %s; complete=%t (maximum 100, newest updated first). This is not a historical snapshot or proof of individual workload/approval duration.", time.Now().UTC().Format(time.RFC3339), project, len(result.Issues), overdue, b, result.IsLast), nil
}
func (j *Jira) Create(ctx context.Context, project, issueType, title, description, marker string) (string, error) {
	if !ProjectPattern.MatchString(project) || !IssueTypePattern.MatchString(issueType) {
		return "", errors.New("invalid Jira target")
	}
	paragraphs := []any{}
	for _, line := range strings.Split(description, "\n") {
		if strings.TrimSpace(line) != "" {
			paragraphs = append(paragraphs, map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": line}}})
		}
	}
	body := map[string]any{"fields": map[string]any{"project": map[string]string{"key": project}, "issuetype": map[string]string{"id": issueType}, "summary": title, "labels": []string{marker}, "description": map[string]any{"type": "doc", "version": 1, "content": paragraphs}}}
	var result struct {
		Key string `json:"key"`
	}
	if err := j.call(ctx, "POST", "/rest/api/3/issue", body, &result); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[0-9]+$`).MatchString(result.Key) {
		return "", errors.New("Jira returned an invalid issue key")
	}
	return j.Site + "/browse/" + result.Key, nil
}
