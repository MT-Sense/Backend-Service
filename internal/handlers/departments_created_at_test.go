package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mt-sense/backend-service/internal/models"
)

func TestDepartmentCreationDateSerialization(t *testing.T) {
	timestamp := time.Date(2024, 2, 10, 9, 0, 0, 0, time.UTC)
	department := models.Department{ID: "department", Name: "Engineering", CreatedAt: timestamp}
	response := newDepartmentWithCode(department)
	if response.CreatedAt == nil || !response.CreatedAt.Equal(timestamp) {
		t.Fatalf("original database date was not preserved: %+v", response)
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["createdAt"] != "2024-02-10T09:00:00Z" {
		t.Fatalf("unexpected wire format: %s", body)
	}

	legacy := newDepartmentWithCode(models.Department{ID: "legacy"})
	body, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if value, exists := decoded["createdAt"]; !exists || value != nil {
		t.Fatalf("unknown date must be explicit null: %s", body)
	}
}
