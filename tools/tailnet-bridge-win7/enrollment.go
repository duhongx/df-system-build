package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// An explicit environment key can replace a used/expired bundled key when a
// node really needs to enroll again. tsnet reuses its persisted identity first.
func enrollmentKey(getenv func(string) string, bundled string) (string, error) {
	for _, candidate := range []string{getenv("TS_AUTHKEY"), getenv("TS_AUTH_KEY"), bundled} {
		if strings.TrimSpace(candidate) != "" {
			return validateAuthKey(candidate)
		}
	}
	return "", nil
}

func validateAuthKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if !strings.HasPrefix(key, "tskey-auth-") || len(key) <= len("tskey-auth-") || len(key) > 1024 {
		return "", errors.New("invalid Tailscale auth key; expected a tskey-auth- enrollment key")
	}
	for _, r := range key {
		if r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("invalid Tailscale auth key; whitespace and control characters are not allowed")
		}
	}
	return key, nil
}

func privateLogMessage(key string, keyOnly bool, format string, args ...any) (string, bool) {
	message := fmt.Sprintf(format, args...)
	if key != "" {
		message = strings.ReplaceAll(message, key, "[REDACTED AUTH KEY]")
	}
	// The key edition never asks the user to fall back to browser sign-in.
	if keyOnly && (strings.Contains(message, "To start this tsnet server,") ||
		strings.Contains(message, "https://login.tailscale.com/a/")) {
		return "", false
	}
	return message, true
}
