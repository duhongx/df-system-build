package service

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractVuePackageBusinessVersionReadsDistConfig(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "04.zip")
	writeZipForBusinessVersionTest(t, zipPath, map[string]string{
		"dist/config.json": `{"xiTongId":"04","version":"2.0.1","date":"2025.11.4_16:20:38","branch":"release_2.15.3_250515","commit":"4f3321c"}`,
		"dist/index.html":  "ok",
	})

	version, err := ExtractPackageBusinessVersion(zipPath, "vue")
	if err != nil {
		t.Fatalf("extract vue business version: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(version), &payload); err != nil {
		t.Fatalf("version should be compact json: %v", err)
	}
	if payload["xiTongId"] != "04" || payload["branch"] != "release_2.15.3_250515" {
		t.Fatalf("unexpected vue version payload: %s", version)
	}
}

func TestExtractJavaPackageBusinessVersionReadsGitProperties(t *testing.T) {
	tmp := t.TempDir()
	jarPath := filepath.Join(tmp, "his-gateway.jar")
	writeZipForBusinessVersionTest(t, jarPath, map[string]string{
		"BOOT-INF/classes/git.properties": "git.branch=release_2.15.3_250515\n" +
			"git.commit.id.abbrev=f1a504b\n" +
			"git.commit.time=2025-10-29 16:01:03\n",
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n",
	})

	version, err := ExtractPackageBusinessVersion(jarPath, "java")
	if err != nil {
		t.Fatalf("extract java business version: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(version), &payload); err != nil {
		t.Fatalf("version should be compact json: %v", err)
	}
	git := payload["git"].(map[string]any)
	commit := git["commit"].(map[string]any)
	if git["branch"] != "release_2.15.3_250515" || commit["id"] != "f1a504b" || commit["time"] != "2025-10-29 16:01:03" {
		t.Fatalf("unexpected java version payload: %s", version)
	}
}

func TestBusinessVersionsEqualComparesJavaGitFields(t *testing.T) {
	packageVersion := `{"git":{"branch":"release_2.15.3_250515","commit":{"id":"f1a504b","time":"2025-10-29 16:01:03"}}}`
	runtimeVersion := `{"git":{"commit":{"time":"2025-10-29 16:01:03","id":"f1a504b"},"branch":"release_2.15.3_250515"}}`

	if !BusinessVersionsEqual("java", packageVersion, runtimeVersion) {
		t.Fatalf("expected java package and runtime versions to match")
	}

	changedRuntime := `{"git":{"commit":{"time":"2025-10-29 16:01:03","id":"xxxxxxx"},"branch":"release_2.15.3_250515"}}`
	if BusinessVersionsEqual("java", packageVersion, changedRuntime) {
		t.Fatalf("expected different commit id to fail comparison")
	}
}

func writeZipForBusinessVersionTest(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(zipPath), 0755); err != nil {
		t.Fatalf("mkdir zip dir: %v", err)
	}
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
}
