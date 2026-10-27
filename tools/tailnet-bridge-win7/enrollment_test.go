package main

import (
	"strings"
	"testing"
)

func TestEnrollmentKeyPriorityAndSafeValidation(t *testing.T) {
	const bundled = "tskey-auth-bundled-fixture"
	const primary = "tskey-auth-primary-fixture"
	const alias = "tskey-auth-alias-fixture"
	for _, tt := range []struct {
		name, primary, alias, bundled, want string
		wantErr                             bool
	}{
		{"bundled", "", "", bundled, bundled, false},
		{"primary wins", primary, alias, bundled, primary, false},
		{"legacy alias", "", alias, bundled, alias, false},
		{"trim pasted newline", "", "", "  " + bundled + "\r\n", bundled, false},
		{"browser edition", "", "", "", "", false},
		{"invalid override must not silently use old key", "bad-private-value", "", bundled, "", true},
		{"API keys cannot enroll", "", "", "tskey-api-private-value", "", true},
		{"reject embedded newlines", "", "", bundled + "\nother", "", true},
		{"reject empty suffix", "", "", "tskey-auth-", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(name string) string {
				if name == "TS_AUTHKEY" {
					return tt.primary
				}
				if name == "TS_AUTH_KEY" {
					return tt.alias
				}
				return ""
			}
			got, err := enrollmentKey(getenv, tt.bundled)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatal("unexpected key selection or validation outcome")
			}
			if err != nil && strings.Contains(err.Error(), "private-value") {
				t.Fatal("validation error exposed the supplied credential")
			}
		})
	}
}

func TestPrivateLogsHideCredentialsAndBrowserFallback(t *testing.T) {
	const key = "tskey-auth-test-fixture"
	message, show := privateLogMessage(key, true, "enrollment failed for %s: %s", key, key)
	if !show || strings.Contains(message, key) || strings.Count(message, "[REDACTED AUTH KEY]") != 2 {
		t.Fatal("credential was exposed or the actual failure was hidden")
	}
	for _, urlMessage := range []string{
		"To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/fixture",
		"Visit https://login.tailscale.com/a/fixture",
	} {
		if _, show := privateLogMessage(key, true, "%s", urlMessage); show {
			t.Fatal("key-only edition displayed a browser sign-in prompt")
		}
		if _, show := privateLogMessage("", false, "%s", urlMessage); !show {
			t.Fatal("normal browser edition lost its login prompt")
		}
	}
	if _, show := privateLogMessage(key, true, "control server unreachable"); !show {
		t.Fatal("network failures must remain visible")
	}
}
