package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/mt-sense/backend-service/internal/models"
	"io"
	"net/http"
	"strings"
	"time"
)

type Candidate struct {
	Playbook    string                      `json:"playbook"`
	Evidence    []models.AutomationEvidence `json:"evidence"`
	Samples     []string                    `json:"samples"`
	MissingData []string                    `json:"missingData"`
}
type Draft struct {
	Playbook     string   `json:"playbook"`
	ProblemFound bool     `json:"problemFound"`
	Title        string   `json:"title"`
	Rationale    string   `json:"rationale"`
	Draft        string   `json:"draft"`
	EvidenceIDs  []string `json:"evidenceIds"`
	MissingData  []string `json:"missingData"`
}

func Plan(ctx context.Context, base, token string, candidates []Candidate) ([]Draft, error) {
	if token == "" {
		return nil, errors.New("configure AUTOMATION_SERVICE_TOKEN in Backend and AI-Service")
	}
	b, _ := json.Marshal(map[string]any{"candidates": candidates})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/automation/propose", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Automation-Token", token)
	resp, err := (&http.Client{Timeout: 75 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("AI-Service unavailable; no proposals were saved")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("AI-Service could not prepare proposals; check service configuration and model availability")
	}
	var out struct {
		Drafts []Draft `json:"drafts"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, errors.New("invalid AI proposal response")
	}
	allowed := map[string]map[string]bool{}
	for _, c := range candidates {
		allowed[c.Playbook] = map[string]bool{}
		for _, e := range c.Evidence {
			allowed[c.Playbook][e.ID] = true
		}
	}
	seen := map[string]bool{}
	for _, d := range out.Drafts {
		ids, ok := allowed[d.Playbook]
		if !ok || seen[d.Playbook] {
			return nil, errors.New("invalid AI playbook")
		}
		seen[d.Playbook] = true
		if !d.ProblemFound {
			continue
		}
		if strings.TrimSpace(d.Title) == "" || len([]rune(d.Title)) > 200 || strings.TrimSpace(d.Draft) == "" || len(d.Draft) > 12000 || len(d.Rationale) > 6000 || len(d.EvidenceIDs) == 0 {
			return nil, errors.New("incomplete AI proposal")
		}
		hasSurvey := false
		for _, id := range d.EvidenceIDs {
			if id == "survey" {
				hasSurvey = true
			}
			if !ids[id] {
				return nil, errors.New("AI cited unknown evidence")
			}
		}
		if !hasSurvey {
			return nil, errors.New("AI proposal lacks survey evidence")
		}
	}
	if len(seen) != len(candidates) {
		return nil, errors.New("AI omitted a requested playbook")
	}
	return out.Drafts, nil
}
