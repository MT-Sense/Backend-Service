package dto

import "testing"

func TestSignupRequestRequiresBaselineFields(t *testing.T) {
	req := SignupRequest{}
	problems := req.Validate()
	if len(problems) == 0 {
		t.Error("empty signup request should be rejected")
	}
}

func TestSignupRequestRejectsReservedSlug(t *testing.T) {
	slug := "mt-sense"
	req := SignupRequest{
		CompanyName:   "Acme Co",
		CompanySlug:   &slug,
		AdminFullName: "Jane HR",
		AdminEmail:    "hr@acme.test",
		AdminPassword: "supersecret1",
	}
	problems := req.Validate()
	if len(problems) == 0 {
		t.Error("reserved slug mt-sense should be rejected")
	}
}

func TestSignupRequestRejectsInvalidEmail(t *testing.T) {
	req := SignupRequest{
		CompanyName:   "Acme Co",
		AdminFullName: "Jane HR",
		AdminEmail:    "not-an-email",
		AdminPassword: "supersecret1",
	}
	problems := req.Validate()
	if len(problems) == 0 {
		t.Error("invalid email should be rejected")
	}
}

func TestSignupRequestAcceptsValidInput(t *testing.T) {
	req := SignupRequest{
		CompanyName:   "Acme Co",
		AdminFullName: "Jane HR",
		AdminEmail:    "hr@acme.test",
		AdminPassword: "supersecret1",
	}
	if problems := req.Validate(); len(problems) > 0 {
		t.Errorf("valid signup request rejected: %v", problems)
	}
}

func TestSignupRequestSlugDerivedFromName(t *testing.T) {
	req := SignupRequest{CompanyName: "Acme Co., Ltd."}
	if got := req.Slug(); got != "acme-co-ltd" {
		t.Errorf("derived slug = %q, want acme-co-ltd", got)
	}
}

func TestSignupRequestSlugPrefersExplicit(t *testing.T) {
	slug := "custom-slug"
	req := SignupRequest{CompanyName: "Acme Co", CompanySlug: &slug}
	if got := req.Slug(); got != "custom-slug" {
		t.Errorf("slug = %q, want custom-slug", got)
	}
}

func TestValidateSlugRejectsUppercaseAndReserved(t *testing.T) {
	if problems := ValidateSlug("Acme-Co"); len(problems) == 0 {
		t.Error("uppercase slug should be rejected")
	}
	if problems := ValidateSlug("api"); len(problems) == 0 {
		t.Error("reserved slug 'api' should be rejected")
	}
	if problems := ValidateSlug("acme-co"); len(problems) > 0 {
		t.Errorf("valid slug rejected: %v", problems)
	}
}

func TestEmployeeRegisterRequestBaselineValidation(t *testing.T) {
	req := EmployeeRegisterRequest{}
	if problems := req.Validate(); len(problems) == 0 {
		t.Error("empty register request should be rejected")
	}

	valid := EmployeeRegisterRequest{
		Code:         "ABC123",
		DepartmentID: "dept-1",
		FirstName:    "Somchai",
		LastName:     "Test",
		Position:     "Engineer",
		Email:        "somchai@acme.test",
		Password:     "supersecret1",
	}
	if problems := valid.Validate(); len(problems) > 0 {
		t.Errorf("valid register request rejected: %v", problems)
	}
	// Department ownership and tenure checks happen in the handler after resolving the company.
}

func TestIsValidTenureBucket(t *testing.T) {
	for _, b := range []string{"under_1y", "1_3y", "3_5y", "5y_plus"} {
		if !IsValidTenureBucket(b) {
			t.Errorf("%q should be a valid tenure bucket", b)
		}
	}
	if IsValidTenureBucket("10y") {
		t.Error("unknown bucket accepted")
	}
}

func TestJoinCodeNormalizesBeforeRegistration(t *testing.T) {
	check := JoinCodeCheckRequest{Code: " demo01 "}
	check.Normalize()
	if check.Code != "DEMO01" || len(check.Validate()) != 0 {
		t.Fatalf("unexpected join check request: %+v", check)
	}
	register := EmployeeRegisterRequest{Code: " demo01 ", DepartmentID: " dept-1 "}
	register.Normalize()
	if register.Code != "DEMO01" || register.DepartmentID != "dept-1" {
		t.Fatalf("unexpected register request normalization: %+v", register)
	}
}
