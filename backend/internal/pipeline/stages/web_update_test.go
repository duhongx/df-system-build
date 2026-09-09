package stages

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestWebUpdatePreservesUnselectedChildrenAndReplacesSelectedDirectory(t *testing.T) {
	for _, withMain := range []bool{false, true} {
		t.Run(map[bool]string{false: "children", true: "main_and_children"}[withMain], func(t *testing.T) {
			root := t.TempDir()
			base, out := filepath.Join(root, "base"), filepath.Join(root, "out")
			mustWriteFile(t, filepath.Join(base, "index.html"), "old-main")
			mustWriteFile(t, filepath.Join(base, "old-main.js"), "old")
			for _, code := range []string{"a", "b", "unregistered"} {
				mustWriteFile(t, filepath.Join(base, "apps", code, "index.html"), code+"-old")
				mustWriteFile(t, filepath.Join(base, "apps", code, "old.js"), code+"-old-js")
			}
			writeTestZip(t, filepath.Join(root, "a.zip"), map[string]string{"dist/index.html": "a-new", "dist/new.js": "a-new-js"})
			artifacts := []model.FrontendArtifact{{FileName: "a.zip", Role: "sub", AppCode: "a"}}
			if withMain {
				writeTestZip(t, filepath.Join(root, "main.zip"), map[string]string{"dist/index.html": "main-new", "dist/apps/b/leaked.js": "must-not-leak"})
				artifacts = append(artifacts, model.FrontendArtifact{FileName: "main.zip", Role: "main"})
			}
			plan, err := prepareWebUpdate(root, filepath.Join(root, "inputs"), artifacts)
			if err != nil {
				t.Fatal(err)
			}
			_, preserved, err := plan.assemble(base, out, false)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(preserved, ",") != "apps/b,apps/unregistered" {
				t.Fatal(preserved)
			}
			assertWebContent(t, filepath.Join(out, "apps/a/index.html"), "a-new")
			assertMissing(t, filepath.Join(out, "apps/a/old.js"))
			assertMissing(t, filepath.Join(out, "apps/b/leaked.js"))
			for _, code := range []string{"b", "unregistered"} {
				before, _ := hashWebTree(filepath.Join(base, "apps", code))
				after, _ := hashWebTree(filepath.Join(out, "apps", code))
				if before != after {
					t.Fatalf("unselected child %s changed", code)
				}
			}
			if withMain {
				assertMissing(t, filepath.Join(out, "old-main.js"))
				assertWebContent(t, filepath.Join(out, "index.html"), "main-new")
			}
			assertWebContent(t, filepath.Join(base, "apps/a/index.html"), "a-old")
		})
	}
}

func TestWebUpdateEmbeddedChildrenAndFirstDeployment(t *testing.T) {
	root := t.TempDir()
	writeTestZip(t, filepath.Join(root, "main.zip"), map[string]string{"index.html": "main", "apps/a/index.html": "a-new"})
	plan, err := prepareWebUpdate(root, filepath.Join(root, "inputs"), []model.FrontendArtifact{{FileName: "main.zip", Role: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := plan.assemble("", filepath.Join(root, "out"), true); err != nil {
		t.Fatal(err)
	}
	assertWebContent(t, filepath.Join(root, "out/apps/a/index.html"), "a-new")
	writeTestZip(t, filepath.Join(root, "a.zip"), map[string]string{"index.html": "duplicate"})
	if _, err := prepareWebUpdate(root, filepath.Join(root, "duplicate"), []model.FrontendArtifact{{FileName: "main.zip", Role: "main"}, {FileName: "a.zip", Role: "sub", AppCode: "a"}}); err == nil {
		t.Fatal("accepted duplicate child")
	}
	child, err := prepareWebUpdate(root, filepath.Join(root, "child"), []model.FrontendArtifact{{FileName: "a.zip", Role: "sub", AppCode: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := child.assemble("", filepath.Join(root, "initial-child"), true); err == nil {
		t.Fatal("accepted initial child-only deployment")
	}
}

func TestWebUpdateRejectsIncompleteOrChangedInputs(t *testing.T) {
	root := t.TempDir()
	writeTestZip(t, filepath.Join(root, "good.zip"), map[string]string{"index.html": "good"})
	writeTestZip(t, filepath.Join(root, "bad.zip"), map[string]string{"assets/main.js": "missing index"})
	tests := [][]model.FrontendArtifact{
		{{FileName: "missing.zip", Role: "main"}},
		{{FileName: "good.zip", Role: "main", SHA256: "incorrect"}},
		{{FileName: "good.zip", Role: "sub", AppCode: "a"}, {FileName: "bad.zip", Role: "sub", AppCode: "b"}},
		{{FileName: "good.zip", Role: "sub", AppCode: "../escape"}},
	}
	for i, inputs := range tests {
		if _, err := prepareWebUpdate(root, filepath.Join(t.TempDir(), "inputs"), inputs); err == nil {
			t.Fatalf("accepted invalid inputs %d", i)
		}
	}
	target := filepath.Join(root, "existing")
	mustWriteFile(t, filepath.Join(target, "index.html"), "keep")
	if err := extractWebZip(nil, filepath.Join(root, "bad.zip"), target); err == nil {
		t.Fatal("accepted ZIP without index")
	}
	assertWebContent(t, filepath.Join(target, "index.html"), "keep")
}

func TestReadWebDeploymentDistinguishesMissingAndFailure(t *testing.T) {
	for _, failure := range []error{errors.New("connection lost"), apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "web-main", errors.New("denied"))} {
		cs := fake.NewSimpleClientset()
		cs.PrependReactor("get", "deployments", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
		if _, err := readWebDeployment(context.Background(), cs, "test"); err == nil {
			t.Fatal("read failure treated as initial deployment")
		}
	}
	if dep, err := readWebDeployment(context.Background(), fake.NewSimpleClientset(), "test"); err != nil || dep != nil {
		t.Fatalf("missing deployment: %v %v", dep, err)
	}
}

func TestWebBaselineRejectsStaleManualImage(t *testing.T) {
	original := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{UID: "uid1", Generation: 4}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web-main", Image: "v1"}}}}}}
	baseline := webBaseline{Exists: true, Cluster: "cluster1", Namespace: "test", UID: "uid1", Generation: 4, Image: "v1"}
	if err := baseline.validate(original, "cluster1", "test"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*appsv1.Deployment){func(d *appsv1.Deployment) { d.Generation++ }, func(d *appsv1.Deployment) { d.UID = "new" }, func(d *appsv1.Deployment) { d.Spec.Template.Spec.Containers[0].Image = "v2" }} {
		dep := original.DeepCopy()
		mutate(dep)
		if err := baseline.validate(dep, "cluster1", "test"); err == nil {
			t.Fatal("accepted stale baseline")
		}
	}
	if baseline.validate(nil, "cluster1", "test") == nil || baseline.validate(original, "cluster2", "test") == nil || baseline.validate(original, "cluster1", "prod") == nil {
		t.Fatal("accepted changed target")
	}
	if (&webBaseline{Cluster: "cluster1", Namespace: "test"}).validate(original, "cluster1", "test") == nil {
		t.Fatal("initial task overwrites existing deployment")
	}
}

func TestUntarRejectsTruncatedFileAndLinks(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "index.html", Mode: 0644, Size: 10, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("incomplete"))
	data := archive.Bytes()
	if err := UntarFromReader(bytes.NewReader(data[:len(data)-2]), t.TempDir()); err == nil {
		t.Fatal("accepted truncated tar")
	}
	archive.Reset()
	writer = tar.NewWriter(&archive)
	_ = writer.WriteHeader(&tar.Header{Name: "index.html", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	_ = writer.Close()
	if err := UntarFromReader(bytes.NewReader(archive.Bytes()), t.TempDir()); err == nil {
		t.Fatal("accepted symlink")
	}
}

func assertWebContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
