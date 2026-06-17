package stages

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExtractWebZipFlattensTopLevelDist(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "web-main.zip")
	writeTestZip(t, zipPath, map[string]string{
		"dist/index.html":    "main",
		"dist/assets/app.js": "js",
		"dist/config.json":   "{}",
		"dist/apps/old/x.js": "old",
	})

	target := filepath.Join(tmp, "html")
	if err := extractWebZip(nil, zipPath, target); err != nil {
		t.Fatalf("extractWebZip failed: %v", err)
	}

	assertFile(t, filepath.Join(target, "index.html"))
	assertFile(t, filepath.Join(target, "assets", "app.js"))
	assertFile(t, filepath.Join(target, "apps", "old", "x.js"))
	assertMissing(t, filepath.Join(target, "dist", "index.html"))
}

func TestExtractWebZipKeepsRootLevelPackage(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "web-main.zip")
	writeTestZip(t, zipPath, map[string]string{
		"index.html":    "main",
		"assets/app.js": "js",
	})

	target := filepath.Join(tmp, "html")
	if err := extractWebZip(nil, zipPath, target); err != nil {
		t.Fatalf("extractWebZip failed: %v", err)
	}

	assertFile(t, filepath.Join(target, "index.html"))
	assertFile(t, filepath.Join(target, "assets", "app.js"))
}

func TestExtractWebZipFlattensSubAppDistIntoAppCodeDir(t *testing.T) {
	tmp := t.TempDir()
	zipPath := filepath.Join(tmp, "00.zip")
	writeTestZip(t, zipPath, map[string]string{
		"dist/index.html":    "sub",
		"dist/assets/app.js": "js",
	})

	target := filepath.Join(tmp, "html", "apps", "00")
	if err := extractWebZip(nil, zipPath, target); err != nil {
		t.Fatalf("extractWebZip failed: %v", err)
	}

	assertFile(t, filepath.Join(target, "index.html"))
	assertFile(t, filepath.Join(target, "assets", "app.js"))
	assertMissing(t, filepath.Join(target, "dist", "index.html"))
}

func TestReplaceWebMainPackagePreservingAppsDeletesOldRootAndKeepsExistingApps(t *testing.T) {
	tmp := t.TempDir()
	htmlDir := filepath.Join(tmp, "html")
	dockerDir := filepath.Join(tmp, "docker-build")
	mustWriteFile(t, filepath.Join(htmlDir, "index.html"), "old-main")
	mustWriteFile(t, filepath.Join(htmlDir, "assets", "old-hash.js"), "old-hash")
	mustWriteFile(t, filepath.Join(htmlDir, "apps", "04", "old-sub.js"), "old-sub")

	zipPath := filepath.Join(tmp, "web-main.zip")
	writeTestZip(t, zipPath, map[string]string{
		"dist/index.html":              "new-main",
		"dist/assets/new-hash.js":      "new-hash",
		"dist/apps/from-package/x.js":  "must-not-survive",
		"dist/apps/from-package/y.css": "must-not-survive",
	})

	if err := replaceWebMainPackagePreservingApps(nil, zipPath, htmlDir, dockerDir); err != nil {
		t.Fatalf("replaceWebMainPackagePreservingApps failed: %v", err)
	}

	assertFile(t, filepath.Join(htmlDir, "index.html"))
	assertFile(t, filepath.Join(htmlDir, "assets", "new-hash.js"))
	assertMissing(t, filepath.Join(htmlDir, "assets", "old-hash.js"))
	assertFile(t, filepath.Join(htmlDir, "apps", "04", "old-sub.js"))
	assertMissing(t, filepath.Join(htmlDir, "apps", "from-package", "x.js"))
	assertMissing(t, filepath.Join(htmlDir, "dist", "index.html"))
}

func TestSelectReadyPodForDeploymentUsesDeploymentSelectorAndReadyPod(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web-main"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"component": "web-main"},
			},
		},
	}
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "first-but-not-matching", Labels: map[string]string{"app": "web-main"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions(true)},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "matching-but-not-ready", Labels: map[string]string{"component": "web-main"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions(false)},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "ready-web-main", Labels: map[string]string{"component": "web-main"}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions(true)},
		},
	}

	pod, ok := selectReadyPodForDeployment(deployment, pods)
	if !ok {
		t.Fatalf("expected a ready pod to be selected")
	}
	if pod.Name != "ready-web-main" {
		t.Fatalf("selected pod = %q, want ready-web-main", pod.Name)
	}
}

func readyConditions(ready bool) []corev1.PodCondition {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
}

func writeTestZip(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("expected file, got directory: %s", path)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected missing path %s, stat err=%v", path, err)
	}
}
