package service

import (
	"testing"

	"pgregory.net/rapid"
)

// Property 11: Settings Concurrency Validation
func TestPropertySettingsConcurrencyValidation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.IntRange(-10, 30).Draw(t, "concurrencyLimit")

		err := validateConcurrencyLimit(value)

		if value >= 1 && value <= 20 {
			if err != nil {
				t.Fatalf("Value %d should be valid, got error: %v", value, err)
			}
		} else {
			if err == nil {
				t.Fatalf("Value %d should be invalid, got nil error", value)
			}
		}
	})
}

func validateConcurrencyLimit(value int) error {
	if value < 1 || value > 20 {
		return &validationError{msg: "并发上限必须在 1-20 之间"}
	}
	return nil
}

// Property: Log retention validation
func TestPropertyLogRetentionValidation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.IntRange(-10, 400).Draw(t, "retentionDays")

		err := validateLogRetention(value)

		if value >= 1 && value <= 365 {
			if err != nil {
				t.Fatalf("Value %d should be valid, got error: %v", value, err)
			}
		} else {
			if err == nil {
				t.Fatalf("Value %d should be invalid, got nil error", value)
			}
		}
	})
}

func validateLogRetention(value int) error {
	if value < 1 || value > 365 {
		return &validationError{msg: "日志保留天数必须在 1-365 之间"}
	}
	return nil
}
