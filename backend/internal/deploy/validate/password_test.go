package validate

import "testing"

func TestPasswordCharsForbidden(t *testing.T) {
	bad := []string{
		"pass word", // space
		"pa$$",       // dollar
		"a'b",        // single quote
		"a\"b",       // double quote
		"a\\b",       // backslash
		"a`b",        // backtick
	}
	for _, v := range bad {
		if err := PasswordChars("db_password", v); err == nil {
			t.Errorf("expected %q to be rejected", v)
		}
	}
}

func TestPasswordCharsAllowed(t *testing.T) {
	good := []string{
		"cloudhis@2123",
		"P@ssw0rd#1",
		"a.b-c_d!e%f",
		"",
	}
	for _, v := range good {
		if err := PasswordChars("db_password", v); err != nil {
			t.Errorf("expected %q to be allowed, got %v", v, err)
		}
	}
}

func TestPasswordParamsScansPasswordKeys(t *testing.T) {
	// Only password-like keys are validated.
	if err := PasswordParams(map[string]any{"db_user": "a b", "note": "x$y"}); err != nil {
		t.Errorf("non-password keys must not be validated: %v", err)
	}
	if err := PasswordParams(map[string]any{"admin_password": "bad pass"}); err == nil {
		t.Error("expected password key with space to be rejected")
	}
	if err := PasswordParams(map[string]any{"db_passwd": "ok@123"}); err != nil {
		t.Errorf("valid passwd should pass: %v", err)
	}
}
