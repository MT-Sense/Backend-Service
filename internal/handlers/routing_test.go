package handlers

import (
	"github.com/mt-sense/backend-service/internal/models"
	"testing"
)

func TestRoutingAmbiguity(t *testing.T) {
	rules := []models.RoutingRule{{Topic: "IT", DepartmentID: "a", Keywords: []string{"tool"}, Enabled: true}, {Topic: "Work", DepartmentID: "b", Keywords: []string{"work"}, Enabled: true}}
	topic, dept, _ := matchRouting(rules, "TOOL unavailable")
	if topic != "IT" || dept != "a" {
		t.Fatal("single rule should suggest owner")
	}
	_, dept, _ = matchRouting(rules, "tool work")
	if dept != "" {
		t.Fatal("ambiguous rules must not choose owner")
	}
	rules[0].Enabled = false
	_, dept, _ = matchRouting(rules, "TOOL")
	if dept != "" {
		t.Fatal("disabled rules must not match")
	}
}
