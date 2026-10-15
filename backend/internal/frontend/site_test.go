package frontend

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, root, file, body string) {
	t.Helper()
	p := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestHTMLReferencesSupportUnquotedAttributesAndSharedCDN(t *testing.T) {
	root := t.TempDir()
	put(t, root, "apps/04/index.html", `<script src=/apps/04/js/app.js></script><link rel='stylesheet' href="./css/app.css"><script src=/apps/cdn/lib/1/lib.js></script>`)
	for _, file := range []string{"apps/04/js/app.js", "apps/04/css/app.css", "apps/cdn/lib/1/lib.js"} {
		put(t, root, file, "asset")
	}
	refs, err := ValidateReferences(root, "apps/04")
	if err != nil || len(refs) != 3 {
		t.Fatalf("%v %v", refs, err)
	}
	_ = os.Remove(filepath.Join(root, "apps/04/js/app.js"))
	if _, err := ValidateReferences(root, "apps/04"); err == nil {
		t.Fatal("missing script accepted")
	}
	put(t, root, "apps/04/index.html", `<script src=/apps/10/js/other.js></script>`)
	if _, err := ValidateReferences(root, "apps/04"); err == nil {
		t.Fatal("wrong application prefix accepted")
	}
}

func TestBuildManifestDetectsMissingOrChangedFilesAndWrongIdentity(t *testing.T) {
	root := t.TempDir()
	put(t, root, "index.html", `<script src=app.js></script>`)
	put(t, root, "app.js", "content")
	if err := WriteBuildManifest(root, BuildManifest{AppName: "web-example", Role: "sub", AppCode: "42", Commit: "verified-source"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBuildManifest(root, "web-example", "sub", "42"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBuildManifest(root, "other", "sub", "42"); err == nil {
		t.Fatal("wrong identity accepted")
	}
	put(t, root, "app.js", "changed")
	if err := ValidateBuildManifest(root, "web-example", "sub", "42"); err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestArchiveRejectsDuplicateTraversalAbsoluteAndCorruptEntries(t *testing.T) {
	for _, names := range [][]string{{"a", "a"}, {"../outside"}, {"/absolute"}, {"a\\b"}, {"a/../b"}} {
		root := t.TempDir()
		var data bytes.Buffer
		w := zip.NewWriter(&data)
		for _, name := range names {
			f, err := w.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.Write([]byte("payload"))
		}
		_ = w.Close()
		file := filepath.Join(root, "bad.zip")
		_ = os.WriteFile(file, data.Bytes(), 0644)
		if err := ValidateArchive(file); err == nil {
			t.Fatalf("accepted %v", names)
		}
	}
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	f, _ := w.CreateHeader(&zip.FileHeader{Name: "later.js", Method: zip.Store})
	_, _ = f.Write([]byte("unique-corrupt-payload"))
	_ = w.Close()
	raw := bytes.Replace(data.Bytes(), []byte("unique-corrupt-payload"), []byte("unique-corrupt-change!"), 1)
	file := filepath.Join(t.TempDir(), "corrupt.zip")
	_ = os.WriteFile(file, raw, 0644)
	if err := ValidateArchive(file); err == nil {
		t.Fatal("CRC corruption accepted")
	}
}
