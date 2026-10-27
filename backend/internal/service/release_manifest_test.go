package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseManifestBusinessContentAndLegacyCompatibility(t *testing.T) {
	raw := `{"appVersion":"2.15.3-门诊修复","releaseTime":"2026-10-26 18:00:00","readme":"修复挂号业务\n调整医保参数","featureList":["支持门诊退费"],"appParamList":[{"paramName":"ENABLE_REFUND","paramDesc":"退费开关"}],"hospital":"滨港医院","business":{"module":"门诊"},"fileList":[{"serviceName":"arbitrary-target"}]}`
	got := ParseReleaseManifest(append([]byte{0xef, 0xbb, 0xbf}, []byte(raw)...))
	if got.ReleaseManifestError != "" || got.AppVersion != "2.15.3-门诊修复" || got.ReleaseTime != "2026-10-26 18:00:00" || got.ReleaseReadme != "修复挂号业务\n调整医保参数" || got.ReleaseManifestJSON != raw {
		t.Fatalf("business content lost: %+v", got)
	}
	// Old release exports use null for optional fields; no fileList is required
	// for a business version that only contains release notes.
	for _, raw := range []string{`{"appVersion":"v1"}`, `{"appVersion":"v1","readme":null,"releaseTime":null,"featureList":null,"appParamList":null}`} {
		if got := ParseReleaseManifest([]byte(raw)); got.ReleaseManifestError != "" || got.AppVersion != "v1" {
			t.Fatalf("legacy metadata rejected: %+v", got)
		}
	}
}

func TestReleaseManifestInvalidContentIsVisibleAndBounded(t *testing.T) {
	for _, raw := range []string{
		`{"appVersion":`, `[]`, `null`, `{}`, `{"appVersion":123}`, `{"appVersion":"  "}`,
		`{"appVersion":"v1","readme":{}}`, `{"appVersion":"\u0000"}`,
		"{\x00}", string([]byte{0xff}), strings.Repeat("x", MaxReleaseManifestBytes+1),
	} {
		if got := ParseReleaseManifest([]byte(raw)); got.ReleaseManifestError == "" || got.AppVersion != "" || strings.ContainsRune(got.ReleaseManifestJSON, 0) {
			t.Fatalf("invalid business metadata accepted or unsafe for storage: %+v", got)
		}
	}
}

func TestReadReleaseManifestMissingNestedAndAmbiguousDirectories(t *testing.T) {
	root := t.TempDir()
	if got := ReadReleaseManifest(root); got.ReleaseManifestError != "" || got.AppVersion != "" {
		t.Fatalf("missing optional manifest is an error: %+v", got)
	}
	nested := filepath.Join(root, "release")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ReleaseManifestFileName), []byte(`{"appVersion":"nested"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadReleaseManifest(root); got.AppVersion != "nested" || got.ReleaseManifestError != "" {
		t.Fatalf("nested release lost: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(root, ReleaseManifestFileName), []byte(`{"appVersion":"root"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadReleaseManifest(root); !strings.Contains(got.ReleaseManifestError, "多个") || got.AppVersion != "" {
		t.Fatalf("ambiguous version silently selected: %+v", got)
	}
	linkRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(nested, ReleaseManifestFileName), filepath.Join(linkRoot, ReleaseManifestFileName)); err != nil {
		t.Fatal(err)
	}
	if got := ReadReleaseManifest(linkRoot); got.ReleaseManifestError == "" {
		t.Fatal("symlink manifest read outside the release directory")
	}
}
