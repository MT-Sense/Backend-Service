package handlers

import (
	"testing"

	"github.com/mt-sense/backend-service/internal/models"
)

func TestIsDesignatedTrainer(t *testing.T) {
	cases := []struct {
		name string
		user models.User
		want bool
	}{
		{"designated HR", models.User{Email: "test@kmitl.ac.th", Role: models.RoleAdmin}, true},
		{"case insensitive email", models.User{Email: " TEST@KMITL.AC.TH ", Role: models.RoleAdmin}, true},
		{"other HR", models.User{Email: "hr@example.com", Role: models.RoleAdmin}, false},
		{"employee with same email", models.User{Email: "test@kmitl.ac.th", Role: models.RoleEmployee}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDesignatedTrainer(&tc.user); got != tc.want {
				t.Fatalf("isDesignatedTrainer() = %v, want %v", got, tc.want)
			}
		})
	}
}
