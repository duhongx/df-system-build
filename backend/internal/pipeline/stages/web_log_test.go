package stages

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/model"
)

func TestWebUpdateFailureLogsExactOperationWithoutReportingCompletion(t *testing.T) {
	root := t.TempDir()
	base, out := filepath.Join(root, "base"), filepath.Join(root, "out")
	mustWriteFile(t, filepath.Join(base, "index.html"), "main")
	mustWriteFile(t, filepath.Join(base, "apps/a/index.html"), "old-a")
	writeTestZip(t, filepath.Join(root, "a.zip"), map[string]string{"dist/index.html": "new-a"})
	var lines []string
	log := webLog(func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	plan, err := prepareWebUpdate(root, filepath.Join(root, "inputs"), []model.FrontendArtifact{{FileName: "a.zip", Role: "sub", AppCode: "a"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate losing the extracted input before copying it into the image.
	if err := os.RemoveAll(plan.children["a"]); err != nil {
		t.Fatal(err)
	}
	_, _, err = plan.assemble(base, out, false)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original filesystem error lost: %v", err)
	}
	for _, want := range []string{"写入新子应用 apps/a失败", plan.children["a"], filepath.Join(out, "apps/a")} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing operation context %q: %v", want, err)
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "开始写入新子应用 apps/a") || !strings.Contains(joined, err.Error()) {
		t.Fatalf("missing start or failure log: %s", joined)
	}
	if strings.Contains(joined, "写入新子应用 apps/a完成") || strings.Contains(joined, "组装完成") {
		t.Fatalf("reported success after failure: %s", joined)
	}
	assertWebContent(t, filepath.Join(base, "apps/a/index.html"), "old-a")
}

func TestWebDiagnosticBufferBoundsOutputWithoutBreakingStream(t *testing.T) {
	var b webDiagnosticBuffer
	for i := 0; i < 3; i++ {
		payload := []byte(strings.Repeat("x", 40000))
		if n, err := b.Write(payload); n != len(payload) || err != nil {
			t.Fatalf("diagnostics interrupted stream: %d %v", n, err)
		}
	}
	if b.Len() != 64*1024 || !strings.Contains(b.String(), "已截断") {
		t.Fatalf("unexpected bounded diagnostics: length=%d", b.Len())
	}
	var copied webDiagnosticBuffer
	reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("x", 120000))}
	if n, err := io.Copy(&copied, reader); n != 120000 || err != nil || copied.Len() != 64*1024 {
		t.Fatalf("io.Copy bypassed diagnostic limit: copied=%d buffered=%d err=%v", n, copied.Len(), err)
	}
}
