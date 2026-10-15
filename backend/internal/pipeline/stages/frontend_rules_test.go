package stages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/frontend"
	"df-build-server/internal/model"
)

func TestNewSubAppCreatesDirectoryPreservesCDNEmptyAndUnknown(t *testing.T) {
	root := t.TempDir()
	base, out := filepath.Join(root, "base"), filepath.Join(root, "out")
	mustWriteFile(t, filepath.Join(base, "index.html"), "main")
	mustWriteFile(t, filepath.Join(base, "apps/04/index.html"), "existing")
	mustWriteFile(t, filepath.Join(base, "apps/cdn/library/1.0/lib.js"), "library")
	mustWriteFile(t, filepath.Join(base, "apps/unknown/file.txt"), "unknown")
	if err := os.MkdirAll(filepath.Join(base, "apps/60"), 0755); err != nil {
		t.Fatal(err)
	}
	before, _ := hashWebTree(base)
	writeTestZip(t, filepath.Join(root, "42.zip"), map[string]string{"index.html": `<script src=/apps/42/js/app.abcd.js></script>`, "js/app.abcd.js": "new", "config.json": `{"xiTongId":"42"}`})
	plan, err := prepareWebUpdate(root, filepath.Join(root, "inputs"), []model.FrontendArtifact{{FileName: "42.zip", Role: "sub", AppCode: "42"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, preserved, err := plan.assemble(base, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(updated, ",") != "apps/42" || strings.Join(preserved, ",") != "apps/04,apps/60,apps/cdn,apps/unknown" {
		t.Fatalf("unexpected scopes: %v %v", updated, preserved)
	}
	if _, err := frontend.ValidateReferences(out, "apps/42"); err != nil {
		t.Fatal(err)
	}
	after, _ := hashWebTree(base)
	if after != before {
		t.Fatal("modified baseline")
	}
	assertWebContent(t, filepath.Join(out, "apps/cdn/library/1.0/lib.js"), "library")
	if info, err := os.Stat(filepath.Join(out, "apps/60")); err != nil || !info.IsDir() {
		t.Fatal("lost empty directory")
	}
}

func TestFrontendRejectsImplicitEmbeddedScopeAndIdentityMismatch(t *testing.T) {
	for _, files := range []map[string]string{
		{"index.html": "main", "apps/04/index.html": "embedded"},
		{"index.html": "main", "apps/04/incomplete.js": "incomplete"},
	} {
		root := t.TempDir()
		writeTestZip(t, filepath.Join(root, "web-main.zip"), files)
		if _, err := prepareWebUpdate(root, filepath.Join(root, "in"), []model.FrontendArtifact{{FileName: "web-main.zip", Role: "main"}}); err == nil {
			t.Fatal("implicit embedded update accepted")
		}
	}
	root := t.TempDir()
	writeTestZip(t, filepath.Join(root, "04.zip"), map[string]string{"index.html": "child", "config.json": `{"xiTongId":"10"}`})
	if _, err := prepareWebUpdate(root, filepath.Join(root, "in"), []model.FrontendArtifact{{FileName: "04.zip", Role: "sub", AppCode: "04"}}); err == nil {
		t.Fatal("renamed other child accepted")
	}
}

func TestCDNUpdateHasNoEntryAndCannotOverwriteVersionedFiles(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	mustWriteFile(t, filepath.Join(base, "index.html"), "main")
	mustWriteFile(t, filepath.Join(base, "apps/cdn/lib/1/file.js"), "one")
	writeTestZip(t, filepath.Join(root, "web-cdn.zip"), map[string]string{"lib/2/file.js": "two"})
	plan, err := prepareWebUpdate(root, filepath.Join(root, "inputs"), []model.FrontendArtifact{{FileName: "web-cdn.zip", Role: "cdn"}})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	if _, _, err := plan.assemble(base, out, false); err != nil {
		t.Fatal(err)
	}
	assertWebContent(t, filepath.Join(out, "apps/cdn/lib/1/file.js"), "one")
	assertWebContent(t, filepath.Join(out, "apps/cdn/lib/2/file.js"), "two")
	writeTestZip(t, filepath.Join(root, "web-cdn.zip"), map[string]string{"lib/1/file.js": "changed"})
	plan, err = prepareWebUpdate(root, filepath.Join(root, "in2"), []model.FrontendArtifact{{FileName: "web-cdn.zip", Role: "cdn"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.assemble(base, filepath.Join(root, "conflict"), false); err == nil {
		t.Fatal("CDN overwritten at same URL")
	}
}

func TestResourcePreparationKeepsOldEntriesAndPreloadsNewResources(t *testing.T) {
	root := t.TempDir()
	base, final, prep := filepath.Join(root, "base"), filepath.Join(root, "final"), filepath.Join(root, "prep")
	for _, dir := range []string{base, final} {
		mustWriteFile(t, filepath.Join(dir, "js/old.1234.js"), "old")
	}
	mustWriteFile(t, filepath.Join(base, "index.html"), "old-entry")
	mustWriteFile(t, filepath.Join(base, "config.json"), "old-config")
	mustWriteFile(t, filepath.Join(final, "index.html"), "new-entry")
	mustWriteFile(t, filepath.Join(final, "config.json"), "new-config")
	mustWriteFile(t, filepath.Join(final, "js/new.5678.js"), "new")
	mustWriteFile(t, filepath.Join(final, "apps/42/index.html"), "new-child-entry")
	mustWriteFile(t, filepath.Join(final, "apps/42/js/app.42.js"), "new-child")
	if err := prepareResourceSite(base, final, prep); err != nil {
		t.Fatal(err)
	}
	assertWebContent(t, filepath.Join(prep, "index.html"), "old-entry")
	assertWebContent(t, filepath.Join(prep, "config.json"), "old-config")
	assertWebContent(t, filepath.Join(prep, "js/new.5678.js"), "new")
	assertWebContent(t, filepath.Join(prep, "apps/42/js/app.42.js"), "new-child")
	assertMissing(t, filepath.Join(prep, "apps/42/index.html"))
}

func TestFrontendRejectsImmutableConflictAndAcceptsLegacyStandalone(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	mustWriteFile(t, filepath.Join(base, "index.html"), "old")
	mustWriteFile(t, filepath.Join(base, "sdk.js"), "old-sdk")
	writeTestZip(t, filepath.Join(root, "web-opm.zip"), map[string]string{"index.html": `<script src=/sdk.js></script>`, "sdk.js": "new-sdk"})
	plan, err := prepareWebUpdate(root, filepath.Join(root, "in"), []model.FrontendArtifact{{FileName: "web-opm.zip", Role: "standalone", AppName: "web-opm"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.assemble(base, filepath.Join(root, "out"), false); err == nil {
		t.Fatal("fixed SDK overwritten")
	}
	if _, _, err := plan.assemble("", filepath.Join(root, "initial"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := frontend.ValidateReferences(filepath.Join(root, "initial"), "."); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMainPackagingExcludesEntireAppsTree(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	mustWriteFile(t, filepath.Join(dist, "index.html"), "main")
	mustWriteFile(t, filepath.Join(dist, "apps/04/index.html"), "child")
	mustWriteFile(t, filepath.Join(dist, "apps/cdn/lib.js"), "cdn")
	if err := zipDirectory(dist, ".", filepath.Join(root, "web-main.zip"), true); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareWebUpdate(root, filepath.Join(root, "in"), []model.FrontendArtifact{{FileName: "web-main.zip", Role: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.children) != 0 || plan.cdnRoot != "" {
		t.Fatal("source main leaked apps")
	}
	assertMissing(t, filepath.Join(plan.mainRoot, "apps"))
}

func TestPreparationImageTagKeepsUniqueSuffixWithinDockerLimit(t *testing.T) {
	one := "registry:8888/prod/web-main:" + strings.Repeat("branch", 18) + "-12345678-20261015123001"
	two := strings.TrimSuffix(one, "01") + "02"
	first, second := preparationImageName(one), preparationImageName(two)
	if first == second || len(first[strings.LastIndex(first, ":")+1:]) > 128 || !strings.HasSuffix(first, "20261015123001-assets") {
		t.Fatalf("invalid preparation tags: %s %s", first, second)
	}
}
