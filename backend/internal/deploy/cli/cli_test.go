package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestGenAndVerify(t *testing.T) {
	dir := t.TempDir()
	resourceDir := filepath.Join(dir, "offline")
	// component "redis" with one file.
	if err := os.MkdirAll(filepath.Join(resourceDir, "redis", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resourceDir, "redis", "bin", "redis-server"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := filepath.Join(dir, "manifest.yml")
	res, err := ManifestGen(resourceDir, manifest, "v1.0.0", nil)
	if err != nil {
		t.Fatalf("manifest gen: %v", err)
	}
	if res.Files != 1 || res.Components != 1 {
		t.Fatalf("expected 1 file/1 component, got %d/%d", res.Files, res.Components)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	// Verify passes when the tree matches the manifest.
	vr, err := Verify(resourceDir, manifest)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.OK {
		t.Fatalf("expected verify ok, missing=%v", vr.Missing)
	}
}
