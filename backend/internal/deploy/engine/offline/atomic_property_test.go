package offline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

// TestPropertyInstallBundleVersionAtomicity checks CP-4: when the bundle
// version does not match the expected one, Install fails and leaves the target
// directory exactly as it was (no partial swap). When it matches, the target
// reflects the new tree.
func TestPropertyInstallBundleVersionAtomicity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dir := mkTempDir(t)
		target := filepath.Join(dir, "resources")
		mustMkdir(t, filepath.Join(target, "old"))
		mustWrite(t, filepath.Join(target, "old", "marker"), "T0")

		expected := rapid.SampledFrom([]string{"v1", "v2", "v3"}).Draw(t, "expected")
		actual := rapid.SampledFrom([]string{"v1", "v2", "v3"}).Draw(t, "actual")

		pkg := filepath.Join(dir, "bundle.tar.gz")
		makeTarGzRapid(t, pkg, map[string]string{"new/file": "T1"}, actual)

		_, err := Install(pkg, target, Options{Clean: true, ExpectedBundleVersion: expected})

		if expected != actual {
			if err == nil {
				t.Fatalf("expected version mismatch error (want %s got %s)", expected, actual)
			}
			if !fileExists(filepath.Join(target, "old", "marker")) {
				t.Fatalf("T0 was modified on a rejected install")
			}
			if fileExists(filepath.Join(target, "new", "file")) {
				t.Fatalf("partial T1 leaked on a rejected install")
			}
		} else {
			if err != nil {
				t.Fatalf("unexpected install error: %v", err)
			}
			if !fileExists(filepath.Join(target, "new", "file")) {
				t.Fatalf("T1 not present after successful install")
			}
		}
	})
}

func mkTempDir(t *rapid.T) string {
	d, err := os.MkdirTemp("", "offline-prop-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func mustMkdir(t *rapid.T, p string) {
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *rapid.T, p, content string) {
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func makeTarGzRapid(t *rapid.T, path string, files map[string]string, bundleVersion string) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	all := map[string]string{}
	for k, v := range files {
		all[k] = v
	}
	if bundleVersion != "" {
		all[BundleVersionFile] = bundleVersion
	}
	for name, content := range all {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	tw.Close()
	gw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tar: %v", err)
	}
}
