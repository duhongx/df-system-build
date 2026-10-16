package stages

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testSiteIdentity() webSiteIdentity {
	return webSiteIdentity{Cluster: "https://cluster", Namespace: "prod", Target: "web-main", UID: "uid-1", Generation: 4, Image: "registry/web-main@sha256:one", ConfigMaps: map[string]string{"nginx": "10"}, ImageIDs: []string{"sha256:one"}}
}

func TestWebSiteStoreCopiesCompleteSiteAndDoesNotShareBuildWrites(t *testing.T) {
	root := t.TempDir()
	identity := testSiteIdentity()
	store, err := newWebSiteStore(filepath.Join(root, "workspaces"), identity)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	mustWriteFile(t, filepath.Join(source, "index.html"), "main")
	mustWriteFile(t, filepath.Join(source, "apps/04/index.html"), "child")
	mustWriteFile(t, filepath.Join(source, ".manifest"), "hidden")
	if err := os.MkdirAll(filepath.Join(source, "apps/60"), 0755); err != nil {
		t.Fatal(err)
	}
	hash, err := hashWebTree(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.save(source, identity, hash, nil); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "build")
	if got, err := store.materialize(identity, output); err != nil || got != hash {
		t.Fatalf("materialize: %s %v", got, err)
	}
	if info, err := os.Stat(filepath.Join(output, "apps/60")); err != nil || !info.IsDir() {
		t.Fatal("empty directory lost")
	}
	assertWebContent(t, filepath.Join(output, ".manifest"), "hidden")
	mustWriteFile(t, filepath.Join(output, "apps/04/index.html"), "failed-build")
	if got, err := store.materialize(identity, filepath.Join(root, "next-build")); err != nil || got != hash {
		t.Fatalf("build writes contaminated saved site: %s %v", got, err)
	}
	state, err := store.read()
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(store.root, state.Snapshot, "html/index.html"), "external-change")
	if _, err := store.materialize(identity, filepath.Join(root, "corrupt")); err == nil {
		t.Fatal("modified saved site reused")
	}
}

func TestWebSiteStoreDoesNotAdvanceOnFailure(t *testing.T) {
	root := t.TempDir()
	identity := testSiteIdentity()
	store, err := newWebSiteStore(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	mustWriteFile(t, filepath.Join(source, "index.html"), "before")
	hash, _ := hashWebTree(source)
	if err := store.save(source, identity, hash, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := store.read()
	oldPointer, _ := os.ReadFile(filepath.Join(store.root, "current.json"))
	mustWriteFile(t, filepath.Join(source, "index.html"), "after")
	nextHash, _ := hashWebTree(source)
	for _, attempt := range []struct {
		name    string
		hash    string
		recheck func() error
	}{
		{"changed-build-files", hash, nil},
		{"external-deployment", nextHash, func() error { return errors.New("Jenkins changed the deployment") }},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			if err := store.save(source, identity, attempt.hash, attempt.recheck); err == nil {
				t.Fatal("invalid candidate saved")
			}
			pointer, _ := os.ReadFile(filepath.Join(store.root, "current.json"))
			if string(pointer) != string(oldPointer) {
				t.Fatal("failed save advanced current pointer")
			}
			if got, err := store.materialize(identity, filepath.Join(root, attempt.name)); err != nil || got != hash {
				t.Fatalf("previous successful site damaged: %s %v", got, err)
			}
		})
	}
	identity.Generation++
	identity.Image = "registry/web-main@sha256:two"
	if err := store.save(source, identity, nextHash, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.materialize(identity, filepath.Join(root, "success")); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, filepath.Join(store.root, before.Snapshot))
	entries, _ := os.ReadDir(store.root)
	if len(entries) != 2 {
		t.Fatalf("stale/staging snapshots accumulated: %v", entries)
	}
}

func TestWebSiteStoreInvalidatesChangesAndSeparatesTargets(t *testing.T) {
	root := t.TempDir()
	identity := testSiteIdentity()
	store, _ := newWebSiteStore(root, identity)
	source := filepath.Join(root, "source")
	mustWriteFile(t, filepath.Join(source, "index.html"), "main")
	hash, _ := hashWebTree(source)
	if err := store.save(source, identity, hash, nil); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*webSiteIdentity){
		"cluster":       func(i *webSiteIdentity) { i.Cluster = "https://another-cluster" },
		"namespace":     func(i *webSiteIdentity) { i.Namespace = "customer" },
		"standalone":    func(i *webSiteIdentity) { i.Target = "web-cdr" },
		"recreated":     func(i *webSiteIdentity) { i.UID = "new-uid" },
		"generation":    func(i *webSiteIdentity) { i.Generation++ },
		"jenkins-image": func(i *webSiteIdentity) { i.Image = "registry/web-main:jenkins-new" },
		"retagged":      func(i *webSiteIdentity) { i.ImageIDs = []string{"sha256:new-content"} },
		"nginx-config":  func(i *webSiteIdentity) { i.ConfigMaps = map[string]string{"nginx": "11"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := identity
			mutate(&changed)
			if _, err := store.materialize(changed, filepath.Join(root, name)); err == nil {
				t.Fatal("changed runtime reused old site")
			}
			other, err := newWebSiteStore(root, changed)
			if err != nil {
				t.Fatal(err)
			}
			separate := name == "cluster" || name == "namespace" || name == "standalone"
			if (other.root != store.root) != separate {
				t.Fatalf("unexpected storage scope: %s", other.root)
			}
		})
	}
	state, _ := store.read()
	state.Snapshot = "../escape"
	if validSiteSnapshot(state.Snapshot) {
		t.Fatal("snapshot traversal accepted")
	}
	current, _ := store.read()
	if !reflect.DeepEqual(current.Identity, identity) {
		t.Fatal("identity checks changed saved state")
	}
}
