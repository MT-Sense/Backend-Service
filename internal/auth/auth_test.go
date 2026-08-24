package auth

import "testing"

func TestJoinCodeLengthAndCharset(t *testing.T) {
	code, err := JoinCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Errorf("join code length = %d, want 6", len(code))
	}
	for _, r := range code {
		found := false
		for _, allowed := range joinCodeCharset {
			if r == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("join code %q contains disallowed character %q", code, r)
		}
	}
}

func TestJoinCodeVariesAcrossCalls(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		code, err := JoinCode()
		if err != nil {
			t.Fatal(err)
		}
		seen[code] = true
	}
	if len(seen) < 15 {
		t.Errorf("only %d unique codes out of 20 calls — suspiciously low entropy", len(seen))
	}
}
