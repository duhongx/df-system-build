package handler

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"df-build-server/internal/model"
)

func TestSafeArtifactFileNameStripsPathAndRejectsUnsupportedExt(t *testing.T) {
	name, err := safeArtifactFileName("../../web-main.zip")
	if err != nil {
		t.Fatalf("expected zip filename to be accepted: %v", err)
	}
	if name != "web-main.zip" {
		t.Fatalf("expected basename web-main.zip, got %q", name)
	}

	if _, err := safeArtifactFileName("web-main.tar.gz"); err == nil {
		t.Fatalf("expected unsupported extension to be rejected")
	}
}

func TestResolveBatchSourceDirRejectsPathOutsideUploadRoot(t *testing.T) {
	outside := t.TempDir()
	if _, err := resolveBatchSourceDir(outside, ""); err == nil {
		t.Fatalf("expected source dir outside batch upload root to be rejected")
	}
}

func TestResolveBatchSourceDirRequiresBatchOrSourceDir(t *testing.T) {
	if _, err := resolveBatchSourceDir("", ""); err == nil {
		t.Fatalf("expected empty source dir and batch id to be rejected")
	}
}

func TestResolveBatchSourceDirAllowsBatchIDUnderUploadRoot(t *testing.T) {
	dir, err := resolveBatchSourceDir("", "batch-123")
	if err != nil {
		t.Fatalf("expected batch id to resolve: %v", err)
	}
	if filepath.Base(dir) != "batch-123" {
		t.Fatalf("expected resolved dir to end with batch id, got %s", dir)
	}
}

func TestValidateExecuteArtifactRejectsMismatchedApp(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "other-service.jar")
	if err := createTestZip(artifact); err != nil {
		t.Fatalf("create test jar: %v", err)
	}

	app := model.Application{AppName: "his-gateway", AppType: "java"}
	if err := validateExecuteArtifact(dir, "other-service.jar", app); err == nil {
		t.Fatalf("expected mismatched artifact/app pair to be rejected")
	}
}

func TestCopyFileSimpleReturnsErrorForMissingSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "target.jar")
	if err := copyFileSimple(filepath.Join(t.TempDir(), "missing.jar"), dst); err == nil {
		t.Fatalf("expected missing source copy to fail")
	}
}

func createTestZip(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		zw.Close()
		return err
	}
	if _, err := w.Write([]byte("Manifest-Version: 1.0\n")); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}
